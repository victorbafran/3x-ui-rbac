package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// Provisioning the front machine. The panel reaches it over SSH with the key
// enrollment left behind — the same machinery the exit servers use, because
// the operator should not have two different ways to hand the panel a server.
//
// What goes on the machine is the tunnel and nothing else: no panel, no
// accounts, one config and one unit per leg. An operator who stops renting it
// should be able to see that what is there is a tunnel and only a tunnel.

// Release is the Backhaul version the panel installs. Pinned rather than
// "latest": an operator standing up a second front months later should get
// the same binary as the first, or the two ends can disagree about the
// protocol and the tunnel simply never completes.
const Release = "v0.7.2"

// The step both install scripts share once the binary is in place: write the
// config and the unit, and restart only when either changed or the unit is not
// running. The reconcile that keeps a front's ports current runs this every
// minute; a script that restarted unconditionally would drop every customer
// on the tunnel every minute while reporting success.
const applyStep = `
umask 077
mkdir -p "$(dirname "$CONF_PATH")"
# base64 rather than a literal: the config has newlines and quotes in it,
# and quoting that through a shell is where the mistakes live.
NEW_CONF="$CONF_PATH.new"
printf %s "$CONF_B64" | base64 -d > "$NEW_CONF"
CHANGED=0
if cmp -s "$NEW_CONF" "$CONF_PATH" 2>/dev/null; then
  rm -f "$NEW_CONF"
else
  mv -f "$NEW_CONF" "$CONF_PATH"
  CHANGED=1
fi

UNIT_FILE="/etc/systemd/system/${UNIT}.service"
NEW_UNIT="$UNIT_FILE.new"
cat > "$NEW_UNIT" <<EOF
[Unit]
Description=$DESCRIPTION ($UNIT)
After=network.target

[Service]
Type=simple
ExecStart=$BIN -c $CONF_PATH
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
chmod 644 "$NEW_UNIT"
if cmp -s "$NEW_UNIT" "$UNIT_FILE" 2>/dev/null; then
  rm -f "$NEW_UNIT"
else
  mv -f "$NEW_UNIT" "$UNIT_FILE"
  systemctl daemon-reload
  CHANGED=1
fi
systemctl enable -q "$UNIT" 2>/dev/null || true

# Staging lets go of a changed leg's ports before any leg binds them again.
if [ "$MODE" = stage ]; then
  if [ "$CHANGED" = 1 ] || [ "$NEED" = 1 ]; then
    systemctl stop "$UNIT" 2>/dev/null || true
    echo "stopped $UNIT to change it"
  fi
  exit 0
fi

# A rebuild that produces the same tunnel must not drop the customers on it.
if [ "$CHANGED" = 1 ] || [ "$NEED" = 1 ] || ! systemctl is-active -q "$UNIT"; then
  systemctl restart "$UNIT"
  echo "restarted $UNIT"
fi

# Report what is true rather than that the command returned. A unit that
# started and exited looks identical to one that is serving, and the operator
# would be told the tunnel was up.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if ss -lnt 2>/dev/null | grep -q ":${TUNNEL_PORT} " || ss -lnu 2>/dev/null | grep -q ":${TUNNEL_PORT} "; then
    echo "listening on ${TUNNEL_PORT}"
    exit 0
  fi
  sleep 1
done
echo "the tunnel started but nothing is listening on ${TUNNEL_PORT}"
systemctl --no-pager -n 10 status "$UNIT" 2>&1 | tail -10
exit 1
`

// remoteScript installs Backhaul on the front machine and runs one leg.
//
// Written to be run again safely. An operator who re-saves a tunnel, or a
// reconcile after a reboot, must end up with the same single unit rather than
// a second one beside it.
const remoteScript = `#!/bin/bash
set -eu
CONF_PATH="$1"; UNIT="$2"; VERSION="$3"; TUNNEL_PORT="$4"; CONF_B64="$5"; MODE="${6:-apply}"
BIN=/usr/local/bin/backhaul
DESCRIPTION="Backhaul tunnel for x-ui"
export DEBIAN_FRONTEND=noninteractive

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "backhaul publishes no build for $(uname -m)"; exit 1 ;;
esac

command -v curl >/dev/null 2>&1 || \
  (apt-get update -qq && apt-get install -y -qq curl tar) >/dev/null 2>&1 || \
  (yum install -y -q curl tar) >/dev/null 2>&1 || true
command -v curl >/dev/null 2>&1 || { echo "no curl on this host"; exit 1; }

# Only when it is missing or a different version: re-running must not pull a
# binary down every reconcile, and must not swap it under a live tunnel.
NEED=1
if [ -x "$BIN" ]; then
  if "$BIN" -v 2>/dev/null | grep -q "${VERSION#v}"; then NEED=0; fi
fi

if [ "$NEED" = 1 ]; then
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  BASE="https://github.com/Musixal/Backhaul/releases/download/$VERSION"
  FILE="backhaul_linux_${ARCH}.tar.gz"
  curl -fsSL "$BASE/$FILE" -o "$TMP/$FILE" || { echo "could not download $FILE"; exit 1; }
  # The checksum is the whole reason this is safe to run: without it the
  # panel would be executing whatever answered the request.
  curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" || { echo "could not download checksums"; exit 1; }
  ( cd "$TMP" && grep " $FILE\$" checksums.txt | sha256sum -c - >/dev/null 2>&1 ) \
    || { echo "the downloaded backhaul does not match its published checksum"; exit 1; }
  tar -xzf "$TMP/$FILE" -C "$TMP" backhaul 2>/dev/null || tar -xzf "$TMP/$FILE" -C "$TMP"
  [ -f "$TMP/backhaul" ] || { echo "the archive held no backhaul binary"; exit 1; }
  install -m 0755 "$TMP/backhaul" "$BIN"
fi
` + applyStep

// backpackRemoteScript runs one BackPack tunnel. The binary is not fetched
// here: the panel downloads it, checks BackPack's release signature, and
// uploads it, because a front in Iran may not reach GitHub at all. It is
// installed under its own name so a BackPack the operator runs from
// /usr/local/bin/backpack is never touched.
const backpackRemoteScript = `#!/bin/bash
set -eu
CONF_PATH="$1"; UNIT="$2"; TUNNEL_PORT="$3"; CONF_B64="$4"; UPLOADED="${5:-}"; MODE="${6:-apply}"
BIN=/usr/local/bin/x-ui-backpack
DESCRIPTION="BackPack tunnel for x-ui"
NEED=0
# Every leg is told of a new binary; the first to see the file installs it.
if [ -n "$UPLOADED" ]; then
  if [ -f "$UPLOADED" ]; then
    install -m 0755 "$UPLOADED" "$BIN"
    rm -f "$UPLOADED"
  fi
  NEED=1
fi
[ -x "$BIN" ] || { echo "no backpack binary on this host"; exit 1; }
` + applyStep

// teardownScript takes the panel's footprint off a machine being given back.
// A front released with a live tunnel is a machine still carrying somebody's
// customers after it has changed hands.
const teardownScript = `#!/bin/bash
set -u
CONF_PATH="$1"; UNIT="$2"
if [ ! -e "/etc/systemd/system/${UNIT}.service" ] && [ ! -e "$CONF_PATH" ]; then
  echo "nothing to remove"
  exit 0
fi
systemctl disable -q --now "$UNIT" 2>/dev/null || true
rm -f "/etc/systemd/system/${UNIT}.service" "$CONF_PATH"
systemctl daemon-reload 2>/dev/null || true
# The binary stays only if another tunnel is still using it.
if ! ls /etc/systemd/system/backhaul-x-ui-*.service >/dev/null 2>&1; then
  rm -f /usr/local/bin/backhaul
  rmdir /etc/backhaul 2>/dev/null || true
fi
# The device-limit bans go with the last tunnel of either tool.
if ! ls /etc/systemd/system/backhaul-x-ui-*.service >/dev/null 2>&1 &&
  ! ls /etc/systemd/system/x-ui-backpack-*.service >/dev/null 2>&1; then
  nft delete table inet x_ui_limit 2>/dev/null || true
fi
echo "tunnel removed"
`

// backpackTeardownScript is teardownScript for BackPack. It removes only the
// panel's own binary, x-ui-backpack, and only once no panel tunnel uses it.
const backpackTeardownScript = `#!/bin/bash
set -u
CONF_PATH="$1"; UNIT="$2"
if [ ! -e "/etc/systemd/system/${UNIT}.service" ] && [ ! -e "$CONF_PATH" ]; then
  echo "nothing to remove"
  exit 0
fi
systemctl disable -q --now "$UNIT" 2>/dev/null || true
rm -f "/etc/systemd/system/${UNIT}.service" "$CONF_PATH"
systemctl daemon-reload 2>/dev/null || true
if ! ls /etc/systemd/system/x-ui-backpack-*.service >/dev/null 2>&1; then
  rm -f /usr/local/bin/x-ui-backpack
  rmdir /etc/x-ui-backpack 2>/dev/null || true
fi
# The device-limit bans go with the last tunnel of either tool.
if ! ls /etc/systemd/system/backhaul-x-ui-*.service >/dev/null 2>&1 &&
  ! ls /etc/systemd/system/x-ui-backpack-*.service >/dev/null 2>&1; then
  nft delete table inet x_ui_limit 2>/dev/null || true
fi
echo "tunnel removed"
`

// Front is what Provision needs to reach and configure the near machine.
type Front struct {
	Pair        Pair
	Credentials relay.Credentials
}

// remote is the part of an SSH session that provisioning uses.
type remote interface {
	Run(ctx context.Context, command string) (string, error)
	Upload(ctx context.Context, path string, data []byte) error
	RunScript(ctx context.Context, name, body string, args ...string) (string, error)
}

// Provision brings every leg of a front to what the panel wants and returns
// once each leg's port is listening, not merely once a command exited zero.
func Provision(ctx context.Context, f Front) error {
	client, err := relay.Dial(ctx, f.Credentials)
	if err != nil {
		return fmt.Errorf("reach the tunnel server: %w", err)
	}
	defer client.Close()
	uploaded := ""
	if f.Pair.Tool.norm() == ToolBackPack {
		if uploaded, err = pushBackPack(ctx, client); err != nil {
			return err
		}
	}
	return provisionLegs(ctx, client, f.Pair, uploaded)
}

// provisionLegs stops every changed leg and removes every unwanted one before
// any starts: BackPack skips a busy forwarded port for good, so a port moving
// between legs must be let go before it is bound again.
func provisionLegs(ctx context.Context, r remote, p Pair, uploaded string) error {
	legs := Legs(p)
	configs := make([]string, len(legs))
	for i, leg := range legs {
		config, err := ServerConfig(leg)
		if err != nil {
			return err
		}
		configs[i] = config
	}
	for i, leg := range legs {
		if out, err := runLeg(ctx, r, leg, configs[i], uploaded, "stage"); err != nil {
			return fmt.Errorf("set up the tunnel: %w: %s", err, lastLine(out))
		}
	}
	if err := removeLegs(ctx, r, unwantedLegs(p, legs)); err != nil {
		return err
	}
	for i, leg := range legs {
		out, err := runLeg(ctx, r, leg, configs[i], "", "apply")
		if err != nil {
			return fmt.Errorf("set up the tunnel: %w: %s", err, lastLine(out))
		}
		if !strings.Contains(out, "listening on") {
			return fmt.Errorf("the tunnel did not come up: %s", lastLine(out))
		}
	}
	return nil
}

func runLeg(ctx context.Context, r remote, leg Pair, config, uploaded, mode string) (string, error) {
	b64 := base64.StdEncoding.EncodeToString([]byte(config))
	port := strconv.Itoa(leg.TunnelPort)
	if leg.Tool.norm() == ToolBackPack {
		return r.RunScript(ctx, "x-ui-backpack.sh", backpackRemoteScript,
			leg.remoteConfigPath(), leg.unitName(), port, b64, uploaded, mode)
	}
	return r.RunScript(ctx, "x-ui-backhaul.sh", remoteScript,
		leg.remoteConfigPath(), leg.unitName(), Release, port, b64, mode)
}

// unwantedLegs is every leg the front could carry that Legs no longer asks for.
func unwantedLegs(p Pair, wanted []Pair) []Pair {
	var out []Pair
	for _, leg := range teardownLegs(p) {
		if !slices.ContainsFunc(wanted, func(w Pair) bool { return w.Key() == leg.Key() }) {
			out = append(out, leg)
		}
	}
	return out
}

func removeLegs(ctx context.Context, r remote, legs []Pair) error {
	for _, leg := range legs {
		script, name := teardownScript, "x-ui-backhaul-down.sh"
		if leg.Tool.norm() == ToolBackPack {
			script, name = backpackTeardownScript, "x-ui-backpack-down.sh"
		}
		if _, err := r.RunScript(ctx, name, script, leg.remoteConfigPath(), leg.unitName()); err != nil {
			return fmt.Errorf("remove the tunnel: %w", err)
		}
	}
	return nil
}

// pushBackPack uploads the verified binary when the front's copy differs and
// says where; compared by content, so a reconcile uploads nothing.
func pushBackPack(ctx context.Context, r remote) (string, error) {
	machine, err := r.Run(ctx, "uname -m")
	if err != nil {
		return "", fmt.Errorf("could not tell what the front runs on: %w", err)
	}
	arch, err := backpackArch(strings.TrimSpace(machine))
	if err != nil {
		return "", err
	}
	local, err := FetchBackPack(ctx, arch)
	if err != nil {
		return "", err
	}
	want, err := fileSHA256(local)
	if err != nil {
		return "", err
	}
	have, _ := r.Run(ctx, "sha256sum /usr/local/bin/"+backpackBinaryName+" 2>/dev/null | cut -d' ' -f1")
	if strings.TrimSpace(have) == want {
		return "", nil
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return "", fmt.Errorf("could not read the verified backpack binary: %w", err)
	}
	uploaded := "/tmp/" + backpackBinaryName + ".upload"
	if err := r.Upload(ctx, uploaded, data); err != nil {
		return "", fmt.Errorf("could not send backpack to the front: %w", err)
	}
	return uploaded, nil
}

// backpackArch maps uname -m to the architecture BackPack names its releases
// after.
func backpackArch(machine string) (string, error) {
	switch machine {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "armv7l", "armv7":
		return "armv7", nil
	case "armv6l", "armv6":
		return "armv6", nil
	case "armv5l", "armv5tel", "armv5tejl", "armv5":
		return "armv5", nil
	case "i386", "i686", "386":
		return "386", nil
	case "s390x":
		return "s390x", nil
	}
	return "", fmt.Errorf("BackPack publishes no build for %s", machine)
}

var hashCache sync.Map // path -> sha256 hex, for a binary that never changes in place

func fileSHA256(path string) (string, error) {
	if v, ok := hashCache.Load(path); ok {
		return v.(string), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	hashCache.Store(path, h)
	return h, nil
}

// teardownLegs is every leg a front could have, whatever its transport is now:
// removing a leg that was never set up is harmless, and leaving one behind
// would keep a port open on a machine that has been given back.
func teardownLegs(p Pair) []Pair {
	p.Tool = p.Tool.norm()
	p.Leg = ""
	second := p
	second.Leg = LegUDP
	if p.Tool == ToolBackPack {
		second.Leg = LegProxy
	}
	return []Pair{p, second}
}

// Teardown removes the panel's footprint from a front machine, every leg.
func Teardown(ctx context.Context, f Front) error {
	client, err := relay.Dial(ctx, f.Credentials)
	if err != nil {
		return fmt.Errorf("reach the tunnel server: %w", err)
	}
	defer client.Close()

	return removeLegs(ctx, client, teardownLegs(f.Pair))
}

// lastLine is what the remote said last, which is where its scripts put the
// reason. The whole output is usually install chatter.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
