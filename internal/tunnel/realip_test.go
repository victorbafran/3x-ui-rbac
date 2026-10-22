package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func realIPPair() Pair {
	p := backpackPair()
	p.Ports = []int{80, 443, 41330}
	p.ProxyPorts = []int{443}
	return p
}

// BackPack sends the header for a whole tunnel or not at all, so the ports
// that want it get a tunnel of their own and leave the main one.
func TestARealIPPortRidesItsOwnBackPackLeg(t *testing.T) {
	legs := Legs(realIPPair())
	if len(legs) != 2 {
		t.Fatalf("got %d legs, want the main one and the real-IP one", len(legs))
	}
	main, px := legs[0], legs[1]
	if !slices.Equal(main.Ports, []int{80, 41330}) || main.ProxyProtocol {
		t.Errorf("main leg carries %v, proxy %v; want 80 and 41330 bare", main.Ports, main.ProxyProtocol)
	}
	if px.Leg != LegProxy || px.TunnelPort != 3081 || !slices.Equal(px.Ports, []int{443}) || !px.ProxyProtocol {
		t.Errorf("real-IP leg is %q on %d carrying %v, proxy %v", px.Leg, px.TunnelPort, px.Ports, px.ProxyProtocol)
	}
	if px.unitName() != "x-ui-backpack-2-proxy" {
		t.Errorf("real-IP leg unit is %q", px.unitName())
	}

	mainConf, _ := ServerConfig(main)
	pxConf, _ := ServerConfig(px)
	if strings.Contains(mainConf, "proxy_protocol") {
		t.Error("the main leg sends a PROXY header its services do not expect")
	}
	if !strings.Contains(pxConf, "proxy_protocol = true") || !strings.Contains(pxConf, `"443=127.0.0.1:443"`) || strings.Contains(pxConf, "41330") {
		t.Errorf("real-IP leg config:\n%s", pxConf)
	}
}

// Backhaul's PROXY support is unmeasured; a port forwarded without the header
// its service demands only looks open.
func TestBackhaulLeavesTheRealIPPortsOut(t *testing.T) {
	p := goodPair()
	p.ProxyPorts = []int{443}
	for _, leg := range Legs(p) {
		if slices.Contains(leg.Ports, 443) || leg.ProxyProtocol {
			t.Errorf("backhaul leg %q carries 443: %v", leg.Leg, leg.Ports)
		}
	}
}

// Reserved while unused, so switching real IP on later never meets an inbound
// already forwarded on that port.
func TestBackPackKeepsItsSecondPortFree(t *testing.T) {
	if got := SecondTunnelPort(backpackPair()); got != 3081 {
		t.Fatalf("SecondTunnelPort = %d, want 3081", got)
	}
}

func TestPortsAreSplitByWhetherTheServiceWantsTheHeader(t *testing.T) {
	reserved := Reserved{TunnelPort: 3080, SecondTunnelPort: 3081, SSHPort: 22}
	plain, proxied := SplitPorts([]Inbound{
		{Port: 443, Enabled: true, Local: true, Proxy: true},
		{Port: 443, Enabled: true, Local: true}, // a UDP neighbour rides the real-IP leg bare
		{Port: 41330, Enabled: true, Local: true},
	}, reserved)
	if !slices.Equal(plain, []int{80, 41330}) || !slices.Equal(proxied, []int{443}) {
		t.Errorf("plain %v, proxied %v; want [80 41330] and [443]", plain, proxied)
	}

	plain, proxied = SplitPorts([]Inbound{{Port: 80, Enabled: true, Local: true, Proxy: true}}, reserved)
	if slices.Contains(plain, 80) || !slices.Equal(proxied, []int{80}) {
		t.Errorf("an inbound on 80 wanting the header: plain %v, proxied %v", plain, proxied)
	}
}

type call struct{ name, unit, mode string }

type fakeRemote struct{ calls []call }

func (f *fakeRemote) Run(context.Context, string) (string, error)  { return "", nil }
func (f *fakeRemote) Upload(context.Context, string, []byte) error { return nil }
func (f *fakeRemote) RunScript(_ context.Context, name, _ string, args ...string) (string, error) {
	c := call{name: name, unit: args[1]}
	switch name {
	case "x-ui-backpack.sh":
		c.mode = args[5]
	case "x-ui-backhaul.sh":
		c.mode = args[5]
	default:
		c.mode = "remove"
	}
	f.calls = append(f.calls, c)
	return "listening on 3080", nil
}

func (f *fakeRemote) order() string {
	var parts []string
	for _, c := range f.calls {
		parts = append(parts, c.mode+" "+c.unit)
	}
	return strings.Join(parts, ", ")
}

// A port moving between legs must be let go before it is bound again, because
// BackPack skips a busy forwarded port for good: every leg is staged first.
func TestEveryLegLetsGoBeforeAnyStarts(t *testing.T) {
	var r fakeRemote
	if err := provisionLegs(t.Context(), &r, realIPPair(), "/tmp/x-ui-backpack.upload"); err != nil {
		t.Fatal(err)
	}
	want := "stage x-ui-backpack-2, stage x-ui-backpack-2-proxy, apply x-ui-backpack-2, apply x-ui-backpack-2-proxy"
	if got := r.order(); got != want {
		t.Errorf("order:\n got %s\nwant %s", got, want)
	}
}

// Switching the last real-IP port off removes that leg before the main one
// takes the port back.
func TestALegNoLongerWantedIsRemovedBeforeTheOthersStart(t *testing.T) {
	var r fakeRemote
	if err := provisionLegs(t.Context(), &r, backpackPair(), ""); err != nil {
		t.Fatal(err)
	}
	want := "stage x-ui-backpack-2, remove x-ui-backpack-2-proxy, apply x-ui-backpack-2"
	if got := r.order(); got != want {
		t.Errorf("order:\n got %s\nwant %s", got, want)
	}
}

// The panel's end of a leg the front no longer has must stop dialling it.
func TestRetainStopsOnlyThisTunnelsUnwantedLegs(t *testing.T) {
	dir := t.TempDir()
	conf := func(key string) string {
		path := filepath.Join(dir, key+".toml")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	m := &Manager{procs: map[string]*managed{
		"2":       {configPath: conf("2")},
		"2-proxy": {configPath: conf("2-proxy")},
		"3-proxy": {configPath: conf("3-proxy")},
	}}
	m.Retain(2, Legs(backpackPair()))

	if _, ok := m.procs["2-proxy"]; ok {
		t.Error("the dropped leg is still managed")
	}
	if _, err := os.Stat(filepath.Join(dir, "2-proxy.toml")); !os.IsNotExist(err) {
		t.Errorf("the dropped leg's config was left on disk: %v", err)
	}
	for _, key := range []string{"2", "3-proxy"} {
		if _, ok := m.procs[key]; !ok {
			t.Errorf("%s was stopped, but it is wanted or another tunnel's", key)
		}
	}
}
