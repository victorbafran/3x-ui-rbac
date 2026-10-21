package tunnel

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// Provisioning the front machine. The panel reaches it over SSH with the key
// enrollment left behind — the same machinery the exit servers use, because
// the operator should not have two different ways to hand the panel a server.
//
// What goes on the machine is Backhaul and nothing else: no panel, no
// accounts, one config and one unit. An operator who stops renting it should
// be able to see that what is there is a tunnel and only a tunnel.

// Release is the Backhaul version the panel installs. Pinned rather than
// "latest": an operator standing up a second front months later should get
// the same binary as the first, or the two ends can disagree about the
// protocol and the tunnel simply never completes.
const Release = "v0.7.2"

// remoteScript installs Backhaul on the front machine and runs this tunnel.
//
// Written to be run again safely. An operator who re-saves a tunnel, or a
// reconcile after a reboot, must end up with the same single unit rather than
// a second one beside it.
const remoteScript = `#!/bin/bash
set -eu
CONF_PATH="$1"; UNIT="$2"; VERSION="$3"; TUNNEL_PORT="$4"; CONF_B64="$5"
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
if [ -x /usr/local/bin/backhaul ]; then
  if /usr/local/bin/backhaul -v 2>/dev/null | grep -q "${VERSION#v}"; then NEED=0; fi
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
  install -m 0755 "$TMP/backhaul" /usr/local/bin/backhaul
fi

umask 077
mkdir -p "$(dirname "$CONF_PATH")"
# base64 rather than a literal: the config has newlines and quotes in it,
# and quoting that through a shell is where the mistakes live.
printf %s "$CONF_B64" | base64 -d > "$CONF_PATH"

cat > "/etc/systemd/system/${UNIT}.service" <<EOF
[Unit]
Description=Backhaul tunnel for x-ui ($UNIT)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/backhaul -c $CONF_PATH
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable -q "$UNIT" 2>/dev/null || true
systemctl restart "$UNIT"

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
echo "backhaul started but nothing is listening on ${TUNNEL_PORT}"
systemctl --no-pager -n 10 status "$UNIT" 2>&1 | tail -10
exit 1
`

// teardownScript takes the panel's footprint off a machine being given back.
// A front released with a live tunnel is a machine still carrying somebody's
// customers after it has changed hands.
const teardownScript = `#!/bin/bash
set -u
CONF_PATH="$1"; UNIT="$2"
systemctl disable -q --now "$UNIT" 2>/dev/null || true
rm -f "/etc/systemd/system/${UNIT}.service" "$CONF_PATH"
systemctl daemon-reload 2>/dev/null || true
# The binary stays only if another tunnel is still using it.
if ! ls /etc/systemd/system/backhaul-x-ui-*.service >/dev/null 2>&1; then
  rm -f /usr/local/bin/backhaul
  rmdir /etc/backhaul 2>/dev/null || true
fi
echo "tunnel removed"
`

// Front is what Provision needs to reach and configure the near machine.
type Front struct {
	Pair        Pair
	Credentials relay.Credentials
}

// Provision installs Backhaul on the front and starts this tunnel. It returns
// only once the tunnel port is actually listening, so "provisioned" means the
// machine is accepting the panel rather than that a command exited zero.
func Provision(ctx context.Context, f Front) error {
	config, err := ServerConfig(f.Pair)
	if err != nil {
		return err
	}
	client, err := relay.Dial(ctx, f.Credentials)
	if err != nil {
		return fmt.Errorf("reach the tunnel server: %w", err)
	}
	defer client.Close()

	out, err := client.RunScript(ctx, "x-ui-backhaul.sh", remoteScript,
		RemoteConfigPath(f.Pair.Id),
		UnitName(f.Pair.Id),
		Release,
		strconv.Itoa(f.Pair.TunnelPort),
		base64.StdEncoding.EncodeToString([]byte(config)),
	)
	if err != nil {
		return fmt.Errorf("set up the tunnel: %w: %s", err, lastLine(out))
	}
	if !strings.Contains(out, "listening on") {
		return fmt.Errorf("the tunnel did not come up: %s", lastLine(out))
	}
	return nil
}

// Teardown removes the panel's footprint from a front machine.
func Teardown(ctx context.Context, f Front) error {
	client, err := relay.Dial(ctx, f.Credentials)
	if err != nil {
		return fmt.Errorf("reach the tunnel server: %w", err)
	}
	defer client.Close()

	if _, err := client.RunScript(ctx, "x-ui-backhaul-down.sh", teardownScript,
		RemoteConfigPath(f.Pair.Id), UnitName(f.Pair.Id)); err != nil {
		return fmt.Errorf("remove the tunnel: %w", err)
	}
	return nil
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
