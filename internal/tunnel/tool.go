package tunnel

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Tool is the program that carries a tunnel. Backhaul came first. BackPack is
// offered beside it for three things Backhaul does not do: it carries UDP on
// the same tunnel, it can fall back from one transport to another, and it can
// hand the service behind it each customer's real address.
//
// Measured on the Iran front on 2026-10-21, BackPack over tcp ran at the raw
// link's rate in both directions, as Backhaul had: the choice between them is
// about what each can do, not about speed.
type Tool string

const (
	ToolBackhaul Tool = "backhaul"
	ToolBackPack Tool = "backpack"
)

// ParseTool reads a stored or requested tool. Empty is Backhaul, which is what
// every front was before there was a choice.
func ParseTool(s string) (Tool, error) {
	switch Tool(strings.TrimSpace(s)) {
	case "", ToolBackhaul:
		return ToolBackhaul, nil
	case ToolBackPack:
		return ToolBackPack, nil
	}
	return "", fmt.Errorf("%q is not a tunnel tool the panel knows; use backhaul or backpack", s)
}

func (t Tool) norm() Tool {
	if t == "" {
		return ToolBackhaul
	}
	return t
}

// BackPack's own carriers beyond the names it shares with Backhaul.
const (
	TransportKCP  Transport = "kcp"
	TransportQUIC Transport = "quic"
)

// backhaulTransports are the eight Backhaul accepts, offered unchanged.
var backhaulTransports = []Transport{
	TransportTCP, TransportTCPMux, TransportWS, TransportWSMux,
	TransportWSS, TransportWSSMux, TransportUDP, TransportUDPoTCP,
}

// backpackTransports are the carriers BackPack runs with nothing but a token.
// It ships more — stealth, pck, xdi and spoof — but those need raw sockets,
// extra keys or a provider that lets forged packets out, and none is offered
// until it has been measured on a real front.
var backpackTransports = []Transport{
	TransportTCP, TransportTCPMux, TransportWS, TransportWSMux,
	TransportWSS, TransportWSSMux, TransportKCP, TransportQUIC, TransportUDP,
}

// TransportsFor lists what a tool accepts, in the order worth trying.
func TransportsFor(tool Tool) []Transport {
	if tool.norm() == ToolBackPack {
		return append([]Transport(nil), backpackTransports...)
	}
	return append([]Transport(nil), backhaulTransports...)
}

// ValidTransportFor refuses a transport the tool does not know. A config
// naming one is rejected by the binary at startup, which shows up as a tunnel
// that will not come up for no visible reason.
func ValidTransportFor(tool Tool, t Transport) error {
	if t == "" {
		return fmt.Errorf("no transport chosen")
	}
	for _, known := range TransportsFor(tool) {
		if known == t {
			return nil
		}
	}
	return fmt.Errorf("%q is not a transport %s knows", t, tool.norm())
}

// tcpCarried is whether a transport keeps TCP connections open to the front,
// which is what the panel's health check looks for. kcp, quic and udp ride
// datagrams and leave nothing in the TCP table.
func tcpCarried(t Transport) bool {
	switch t {
	case TransportTCP, TransportTCPMux, TransportWS, TransportWSMux,
		TransportWSS, TransportWSSMux, TransportUDPoTCP:
		return true
	}
	return false
}

// LegUDP names Backhaul's second tunnel. A customer's UDP — WireGuard,
// Hysteria, TUIC, DTLS for cisco, OpenVPN over UDP — needs to reach the panel
// too, and Backhaul's accept_udp inside a TCP tunnel was measured unstable on
// the real front while a tunnel whose transport is udp delivered every
// datagram. So a Backhaul front carries TCP on its main tunnel and UDP on this
// one, on the next port up. BackPack forwards UDP on its one tunnel and needs
// no second.
const LegUDP = "udp"

// Legs is every tunnel one front needs, the main one first.
func Legs(p Pair) []Pair {
	p.Tool = p.Tool.norm()
	p.Leg = ""
	if p.Tool == ToolBackPack || !tcpCarried(p.Transport) || p.Transport == TransportUDPoTCP {
		// BackPack carries UDP itself; a Backhaul tunnel that already rides
		// udp, or carries UDP over TCP, must not be doubled — two tunnels on
		// one UDP port silently split its traffic between them.
		return []Pair{p}
	}
	udp := p
	udp.Leg = LegUDP
	udp.Transport = TransportUDP
	udp.TunnelPort = p.TunnelPort + 1
	return []Pair{p, udp}
}

// UDPTunnelPort is the port Backhaul's UDP leg listens on, zero when the front
// needs no second leg. Reserved like the main tunnel port, so no inbound on it
// is ever forwarded into a collision.
func UDPTunnelPort(p Pair) int {
	for _, leg := range Legs(p) {
		if leg.Leg == LegUDP {
			return leg.TunnelPort
		}
	}
	return 0
}

// Key names one leg of one tunnel: the id, and the leg after it when there is
// one. Every file and unit is named after it, so two tunnels, or two legs of
// one, can share neither.
func (p Pair) Key() string {
	if p.Leg == "" {
		return strconv.Itoa(p.Id)
	}
	return strconv.Itoa(p.Id) + "-" + p.Leg
}

// unitName is the systemd unit on the front. BackPack's is deliberately not
// "backpack-…": an operator may run BackPack's own manager on the same machine,
// and it treats every backpack-<name> unit and every file in /etc/backpack as
// its own to restart, alert on and edit. The panel's tunnels stay out of its
// sight entirely.
func (p Pair) unitName() string {
	if p.Tool.norm() == ToolBackPack {
		return "x-ui-backpack-" + p.Key()
	}
	return "backhaul-x-ui-" + p.Key()
}

// remoteConfigPath is where that unit reads its config on the front.
func (p Pair) remoteConfigPath() string {
	if p.Tool.norm() == ToolBackPack {
		return "/etc/x-ui-backpack/x-ui-" + p.Key() + ".toml"
	}
	return "/etc/backhaul/x-ui-" + p.Key() + ".toml"
}

// localConfigPath is where the panel keeps its own end's config.
func (p Pair) localConfigPath() string {
	if p.Tool.norm() == ToolBackPack {
		return filepath.Join(runtimeDir(), "backpack-client-"+p.Key()+".toml")
	}
	return filepath.Join(runtimeDir(), "client-"+p.Key()+".toml")
}
