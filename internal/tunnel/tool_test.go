package tunnel

import (
	"strings"
	"testing"
)

func backpackPair() Pair {
	p := goodPair()
	p.Tool = ToolBackPack
	p.Transport = TransportTCP
	return p
}

// Every front before the choice existed was Backhaul, and its stored tool is
// empty; that must keep reading as Backhaul.
func TestAnEmptyToolIsBackhaul(t *testing.T) {
	for _, in := range []string{"", " ", "backhaul"} {
		got, err := ParseTool(in)
		if err != nil || got != ToolBackhaul {
			t.Errorf("ParseTool(%q) = %q, %v; want backhaul", in, got, err)
		}
	}
	if got, err := ParseTool("backpack"); err != nil || got != ToolBackPack {
		t.Errorf("ParseTool(backpack) = %q, %v", got, err)
	}
	if _, err := ParseTool("wireguard"); err == nil {
		t.Error("a tool the panel cannot install was accepted")
	}
}

// Each tool is offered only what it runs. A config naming a transport the
// binary does not know is refused at startup, which reads as a dead tunnel.
func TestEachToolAcceptsOnlyItsOwnTransports(t *testing.T) {
	if err := ValidTransportFor(ToolBackPack, TransportKCP); err != nil {
		t.Errorf("backpack refused kcp: %v", err)
	}
	if err := ValidTransportFor(ToolBackhaul, TransportKCP); err == nil {
		t.Error("backhaul accepted kcp, which it does not have")
	}
	if err := ValidTransportFor(ToolBackhaul, TransportUDPoTCP); err != nil {
		t.Errorf("backhaul refused udpovertcp: %v", err)
	}
	// Measured on the Iran front: stealth hides what the tunnel carries, and
	// carries the PROXY header and UDP. Backhaul has no such transport.
	if err := ValidTransportFor(ToolBackPack, TransportStealth); err != nil {
		t.Errorf("backpack refused stealth: %v", err)
	}
	if err := ValidTransportFor(ToolBackhaul, TransportStealth); err == nil {
		t.Error("backhaul accepted stealth, which it does not have")
	}
	// Not offered until measured on a real front: these need raw sockets,
	// extra keys, or a provider that lets forged packets out.
	for _, unmeasured := range []Transport{"spoof", "pck", "xdi"} {
		if err := ValidTransportFor(ToolBackPack, unmeasured); err == nil {
			t.Errorf("backpack accepted %s before anybody measured it", unmeasured)
		}
	}
}

// The operator's own BackPack manager treats every backpack-<name> unit and
// every file under /etc/backpack as its own to restart and alert on. On the
// test server it restarts a tunnel every 3 minutes 20 seconds. The panel's
// tunnels must be invisible to it.
func TestThePanelsBackPackStaysOutOfBackPacksOwnManagement(t *testing.T) {
	for _, leg := range Legs(backpackPair()) {
		if strings.HasPrefix(leg.unitName(), "backpack-") {
			t.Errorf("unit %q is one BackPack's own manager would claim", leg.unitName())
		}
		if strings.HasPrefix(leg.remoteConfigPath(), "/etc/backpack/") {
			t.Errorf("config %q sits in BackPack's own directory", leg.remoteConfigPath())
		}
	}
	if strings.Contains(backpackRemoteScript, "/usr/local/bin/backpack ") || strings.Contains(backpackRemoteScript, "BIN=/usr/local/bin/backpack\n") {
		t.Error("the install would replace the operator's own backpack binary")
	}
	if !strings.Contains(backpackRemoteScript, "BIN=/usr/local/bin/x-ui-backpack") {
		t.Error("the panel's backpack is not installed under its own name")
	}
}

// Backhaul carries UDP on a second tunnel one port up; BackPack forwards it on
// the one tunnel. Without the second leg every WireGuard, Hysteria and TUIC
// customer behind a Backhaul front is silently dropped.
func TestEveryFrontCarriesUDPOneWayOrTheOther(t *testing.T) {
	legs := Legs(goodPair())
	if len(legs) != 2 {
		t.Fatalf("a backhaul tcp front has %d legs, want 2", len(legs))
	}
	main, udp := legs[0], legs[1]
	if main.Leg != "" || udp.Leg != LegUDP {
		t.Fatalf("legs are %q and %q", main.Leg, udp.Leg)
	}
	if udp.Transport != TransportUDP || udp.TunnelPort != main.TunnelPort+1 {
		t.Fatalf("the udp leg is %s on %d", udp.Transport, udp.TunnelPort)
	}
	if udp.unitName() == main.unitName() || udp.remoteConfigPath() == main.remoteConfigPath() || udp.localConfigPath() == main.localConfigPath() {
		t.Fatal("the two legs share a unit or a config")
	}
	if got := SecondTunnelPort(goodPair()); got != main.TunnelPort+1 {
		t.Fatalf("SecondTunnelPort = %d", got)
	}

	if legs := Legs(backpackPair()); len(legs) != 1 {
		t.Fatalf("a backpack front has %d legs; it carries UDP itself", len(legs))
	}

	// A tunnel that already rides udp, or carries UDP over TCP, must not be
	// doubled: two tunnels on one UDP port silently split its traffic.
	for _, tr := range []Transport{TransportUDP, TransportUDPoTCP} {
		p := goodPair()
		p.Transport = tr
		if legs := Legs(p); len(legs) != 1 {
			t.Errorf("a backhaul %s front got %d legs", tr, len(legs))
		}
	}
}

// The existing Backhaul names do not move: a front set up before this change
// keeps its unit and its config.
func TestBackhaulsMainLegKeepsItsNames(t *testing.T) {
	p := goodPair()
	if p.unitName() != UnitName(p.Id) || p.remoteConfigPath() != RemoteConfigPath(p.Id) {
		t.Fatalf("names moved: %s %s", p.unitName(), p.remoteConfigPath())
	}
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	if p.localConfigPath() != LocalConfigPath(p.Id) {
		t.Fatalf("local config moved: %s", p.localConfigPath())
	}
}

// Removing one tunnel must take every leg of it and nothing of another.
func TestALegBelongsToItsTunnelOnly(t *testing.T) {
	for key, want := range map[string]bool{"7": true, "7-udp": true, "70": false, "17": false, "70-udp": false} {
		if got := ownedBy(key, 7); got != want {
			t.Errorf("ownedBy(%q, 7) = %v, want %v", key, got, want)
		}
	}
}

// The health check looks for a TCP connection, so a datagram carrier must be
// reported as unknown rather than as down.
func TestADatagramCarrierIsNotJudgedByTheTCPTable(t *testing.T) {
	for _, tr := range []Transport{TransportKCP, TransportQUIC, TransportUDP} {
		known, _ := Established(Pair{FrontAddress: "185.226.116.154", TunnelPort: 3090, Transport: tr, Tool: ToolBackPack})
		if known {
			t.Errorf("a %s tunnel was judged from the TCP table it never appears in", tr)
		}
	}
}
