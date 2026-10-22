package tunnel

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// A device over its limit is cut off where it connects. Behind a front that is
// the front: this panel's firewall only ever sees the tunnel's own loopback.

// banScript drops each address on every port but SSH until its timeout ends;
// measured on the Iran front, a banned address timed out on :80, SSH stayed up.
const banScript = `#!/bin/bash
set -eu
SSH_PORT="$1"; BAN_SECONDS="$2"; shift 2
command -v nft >/dev/null 2>&1 || { echo "no nft on this host"; exit 1; }
# One transaction, safe to repeat: the sets keep their members, the chain is
# rebuilt so an SSH port that moved is still let through.
nft -f - <<EOF
add table inet x_ui_limit
add set inet x_ui_limit v4 { type ipv4_addr; flags timeout; }
add set inet x_ui_limit v6 { type ipv6_addr; flags timeout; }
add chain inet x_ui_limit input { type filter hook input priority -5; policy accept; }
flush chain inet x_ui_limit input
add rule inet x_ui_limit input tcp dport $SSH_PORT accept
add rule inet x_ui_limit input ip saddr @v4 drop
add rule inet x_ui_limit input ip6 saddr @v6 drop
EOF
for ip in "$@"; do
  case "$ip" in *:*) set=v6 ;; *) set=v4 ;; esac
  # Re-adding a member keeps its old timeout, so a fresh ban replaces it.
  nft delete element inet x_ui_limit "$set" "{ $ip }" 2>/dev/null || true
  nft add element inet x_ui_limit "$set" "{ $ip timeout ${BAN_SECONDS}s }"
done
echo "banned $#"
`

// banTargets is what a front can drop: public addresses, each once, as nft
// reads them; anything else fails the script or cuts the front off itself.
func banTargets(ips []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
			ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		key := ip.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// Ban drops the addresses on a front for d, its SSH port excepted.
func Ban(ctx context.Context, creds relay.Credentials, ips []string, d time.Duration) error {
	targets := banTargets(ips)
	if len(targets) == 0 {
		return nil
	}
	client, err := relay.Dial(ctx, creds)
	if err != nil {
		return fmt.Errorf("reach the tunnel server: %w", err)
	}
	defer client.Close()
	return ban(ctx, client, creds.Port, targets, d)
}

func ban(ctx context.Context, r remote, sshPort int, targets []string, d time.Duration) error {
	if sshPort <= 0 {
		sshPort = 22
	}
	seconds := max(int(d/time.Second), 1)
	args := append([]string{strconv.Itoa(sshPort), strconv.Itoa(seconds)}, targets...)
	if out, err := r.RunScript(ctx, "x-ui-limit-ban.sh", banScript, args...); err != nil {
		return fmt.Errorf("cut off %d addresses: %w: %s", len(targets), err, lastLine(out))
	}
	return nil
}
