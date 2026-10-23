package tunnel

import (
	"fmt"
	"path/filepath"
	"slices"
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
	// TransportStealth wraps the stream in a Noise layer keyed by the token, so
	// a filter on the way out cannot read the ClientHellos the tunnel carries.
	TransportStealth Transport = "stealth"
)

// backhaulTransports are the eight Backhaul accepts, offered unchanged.
var backhaulTransports = []Transport{
	TransportTCP, TransportTCPMux, TransportWS, TransportWSMux,
	TransportWSS, TransportWSSMux, TransportUDP, TransportUDPoTCP,
}

// backpackTransports are the carriers BackPack runs with nothing but a token,
// stealth first: on the Iran front plain tcp let the filter drop cisco's
// ClientHello, and stealth carried PROXY, UDP and 400-800 Mbit/s like tcp.
// pck, xdi and spoof need raw sockets or forged packets and wait to be measured.
var backpackTransports = []Transport{
	TransportStealth, TransportTCP, TransportTCPMux, TransportWS, TransportWSMux,
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

// LegProxy names BackPack's second tunnel, one port up, which sends a PROXY v2
// header on every connection; BackPack can switch that on only per tunnel.
const LegProxy = "proxy"

// Legs is every tunnel one front needs, the main one first. A port rides
// exactly one leg: two listeners cannot share it on the front.
func Legs(p Pair) []Pair {
	p.Tool = p.Tool.norm()
	p.Leg = ""
	p.ProxyProtocol = false
	proxied := normalisePorts(p.ProxyPorts, p.TunnelPort)
	p.ProxyPorts = nil
	p.Ports = without(p.Ports, proxied)

	if p.Tool == ToolBackPack {
		if len(proxied) == 0 {
			return []Pair{p}
		}
		px := p
		px.Leg = LegProxy
		px.TunnelPort = p.TunnelPort + 1
		px.Ports = proxied
		px.ProxyProtocol = true
		return []Pair{p, px}
	}
	// Backhaul's own PROXY support is unmeasured, so its fronts leave those
	// ports out: forwarded without the header, their services refuse anyway.
	if !tcpCarried(p.Transport) || p.Transport == TransportUDPoTCP {
		// A Backhaul tunnel that already rides udp, or carries UDP over TCP,
		// must not be doubled: two tunnels on one UDP port split its traffic.
		return []Pair{p}
	}
	udp := p
	udp.Leg = LegUDP
	udp.Transport = TransportUDP
	udp.TunnelPort = p.TunnelPort + 1
	return []Pair{p, udp}
}

// SecondTunnelPort is the port one above the tunnel's, which Backhaul's UDP
// leg or BackPack's proxy leg listens on, and zero when the front can have
// neither. BackPack's is reserved even while unused, so switching real client
// IP on later never collides with a forwarded inbound.
func SecondTunnelPort(p Pair) int {
	if p.Tool.norm() == ToolBackPack {
		return p.TunnelPort + 1
	}
	for _, leg := range Legs(p) {
		if leg.Leg == LegUDP {
			return leg.TunnelPort
		}
	}
	return 0
}

func without(ports, drop []int) []int {
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if !slices.Contains(drop, port) {
			out = append(out, port)
		}
	}
	return out
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
