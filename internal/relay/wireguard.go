package relay

import (
	"fmt"
	"strings"
)

// A relay carries one WireGuard tunnel to the panel and nothing else. The
// panel end is an interface named after the relay's id; the relay end does the
// address translation, because a country's traffic has to appear to come from
// that country and nowhere else.

// exitPools is every address a cisco location can ever be handed. The panel
// derives location pools inside 10.248.0.0/13 and nothing else uses that
// block, so the relay can be told about the whole range once and never has to
// be reconfigured again when a country is added to an inbound.
const exitPools = "10.248.0.0/13"

// InterfaceName is the panel-side interface for one relay. Derived from the id
// so it cannot collide with an operator's own wg0, and short enough for the
// 15-character limit Linux puts on an interface name.
func InterfaceName(id int) string { return fmt.Sprintf("wgr%d", id) }

// tunnelAddress is the point-to-point pair for one relay: .1 on the panel,
// .2 on the relay. One /24 per relay out of 10.200.0.0/16, which leaves room
// for 254 of them and stays clear of the 10.248 block the pools use.
func tunnelAddress(id int) (panelSide, relaySide string) {
	n := id % 255
	if n == 0 {
		n = 255
	}
	return fmt.Sprintf("10.200.%d.1", n), fmt.Sprintf("10.200.%d.2", n)
}

// ListenPort is the UDP port the relay listens on. Derived from the id so two
// relays enrolled on one machine — which happens only in testing — do not
// collide, and high enough to stay out of the way of whatever else is there.
func ListenPort(id int) int { return 51820 + (id % 1000) }

// remoteScript is what runs on the relay. It installs WireGuard, writes one
// config, turns on forwarding and masquerades the panel's pools out of the
// relay's own address. Nothing of the panel is installed and nothing listens
// except WireGuard itself.
//
// It is written to be run again safely: an operator who re-saves a relay, or a
// reconcile after a reboot, must not end up with the rule twice or a second
// interface.
const remoteScript = `#!/bin/bash
set -eu
PRIV="$1"; PEER_PUB="$2"; ADDR="$3"; PORT="$4"; POOLS="$5"; IFACE="$6"
export DEBIAN_FRONTEND=noninteractive

if ! command -v wg >/dev/null 2>&1; then
  (apt-get update -qq && apt-get install -y -qq wireguard-tools iptables) >/dev/null 2>&1 \
    || (yum install -y -q wireguard-tools iptables) >/dev/null 2>&1 \
    || { echo "could not install wireguard-tools"; exit 1; }
fi
command -v wg >/dev/null 2>&1 || { echo "wireguard-tools is still missing"; exit 1; }

umask 077
mkdir -p /etc/wireguard
cat > "/etc/wireguard/$IFACE.conf" <<EOF
# written by x-ui. This host only forwards traffic; nothing else is installed.
[Interface]
Address = $ADDR/24
ListenPort = $PORT
PrivateKey = $PRIV

[Peer]
PublicKey = $PEER_PUB
AllowedIPs = $POOLS
EOF

sysctl -qw net.ipv4.ip_forward=1
grep -q "^net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null || echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

NIC=$(ip route show default | awk '{print $5}' | head -1)
[ -n "$NIC" ] || { echo "this host has no default route"; exit 1; }
iptables -t nat -C POSTROUTING -s "$POOLS" -o "$NIC" -j MASQUERADE 2>/dev/null \
  || iptables -t nat -A POSTROUTING -s "$POOLS" -o "$NIC" -j MASQUERADE
iptables -C FORWARD -s "$POOLS" -j ACCEPT 2>/dev/null || iptables -I FORWARD -s "$POOLS" -j ACCEPT
iptables -C FORWARD -d "$POOLS" -j ACCEPT 2>/dev/null || iptables -I FORWARD -d "$POOLS" -j ACCEPT

systemctl enable -q "wg-quick@$IFACE" 2>/dev/null || true
systemctl restart "wg-quick@$IFACE"
sleep 1
ip -4 -o addr show "$IFACE" | awk '{print "address " $4}'
echo "exit ready on $NIC"
`

// teardownScript removes everything the panel put on a relay, for an operator
// who is giving the server back. A relay released with a live WireGuard
// interface and a masquerade rule is a machine still forwarding somebody's
// traffic after it has changed hands.
const teardownScript = `#!/bin/bash
set -u
POOLS="$1"; IFACE="$2"
systemctl disable -q --now "wg-quick@$IFACE" 2>/dev/null || true
rm -f "/etc/wireguard/$IFACE.conf"
NIC=$(ip route show default | awk '{print $5}' | head -1)
while iptables -t nat -C POSTROUTING -s "$POOLS" -o "$NIC" -j MASQUERADE 2>/dev/null; do
  iptables -t nat -D POSTROUTING -s "$POOLS" -o "$NIC" -j MASQUERADE || break
done
while iptables -C FORWARD -s "$POOLS" -j ACCEPT 2>/dev/null; do
  iptables -D FORWARD -s "$POOLS" -j ACCEPT || break
done
while iptables -C FORWARD -d "$POOLS" -j ACCEPT 2>/dev/null; do
  iptables -D FORWARD -d "$POOLS" -j ACCEPT || break
done
echo "removed"
`

// localConfig is the panel's side of the tunnel.
//
// Table = off is the whole safety of this feature. wg-quick's default is to
// write routes for AllowedIPs, and AllowedIPs here is 0.0.0.0/0 because the
// tunnel must be able to carry any destination a customer asks for. Letting it
// install that as a route would move the panel's own traffic — and the
// operator's SSH session — into a tunnel to a rented VPS. Instead nothing is
// routed by default, and each country's pool is steered in by the rule the
// ocserv side installs for it.
func localConfig(privateKey, peerPublicKey, address, endpoint string) string {
	var b strings.Builder
	b.WriteString("# written by x-ui — edits are overwritten\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s/24\n", address)
	fmt.Fprintf(&b, "PrivateKey = %s\n", privateKey)
	b.WriteString("Table = off\n\n")
	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", peerPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	b.WriteString("AllowedIPs = 0.0.0.0/0\n")
	b.WriteString("PersistentKeepalive = 25\n")
	return b.String()
}
