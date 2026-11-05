package service

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// stubFrontAcct stands in for the panel's netfilter: every fit's fronts are kept, and
// read answers for the fronts a read names, or a read fails the test when read is nil.
func stubFrontAcct(t *testing.T, read func() map[int]map[string]tunnel.LegMetrics) *[][]tunnel.Pair {
	t.Helper()
	fits := &[][]tunnel.Pair{}
	oldFit, oldRead := fitFrontAcct, readFrontAcct
	t.Cleanup(func() { fitFrontAcct, readFrontAcct = oldFit, oldRead })
	fitFrontAcct = func(fronts []tunnel.Pair) error {
		*fits = append(*fits, fronts)
		return nil
	}
	readFrontAcct = func(fronts []tunnel.Pair, _ time.Time) (map[int]map[string]tunnel.LegMetrics, error) {
		if read == nil {
			t.Error("the firewall was read with no enabled front to count")
			return nil, nil
		}
		counters, named := read(), map[int]map[string]tunnel.LegMetrics{}
		for _, f := range fronts {
			if c, ok := counters[f.Id]; ok {
				named[f.Id] = c
			}
		}
		return named, nil
	}
	return fits
}

// A Backhaul front's totals come from the firewall's counters, each direction on its
// own: a rule recreated after a reboot, or one alone, counts from zero and no other does.
func TestABackhaulFrontIsCountedFromTheFirewallEachDirectionOnItsOwn(t *testing.T) {
	r := seedBackPackFront(t)
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).Update("tunnel", "backhaul").Error; err != nil {
		t.Fatal(err)
	}
	main, udp := strconv.Itoa(r.Id), strconv.Itoa(r.Id)+"-udp"
	var counters map[string]tunnel.LegMetrics
	stubFrontAcct(t, func() map[int]map[string]tunnel.LegMetrics {
		return map[int]map[string]tunnel.LegMetrics{r.Id: counters}
	})
	taken := time.Now().Add(-2 * time.Minute)
	reading := func(out, in, udpOut, udpIn int64) map[string]tunnel.LegMetrics {
		taken = taken.Add(30 * time.Second)
		return map[string]tunnel.LegMetrics{
			main + ":out": {Taken: taken, BytesOut: out}, main + ":in": {Taken: taken, BytesIn: in},
			udp + ":out": {Taken: taken, BytesOut: udpOut}, udp + ":in": {Taken: taken, BytesIn: udpIn},
		}
	}
	for _, step := range []struct {
		name            string
		read            map[string]tunnel.LegMetrics
		wantIn, wantOut int64
	}{
		{"the first reading counts in full", reading(5000, 1000, 300, 100), 1100, 5300},
		{"the next adds what moved", reading(8000, 1600, 300, 100), 1700, 8300},
		{"rules recreated after a reboot count from zero", reading(50, 20, 0, 0), 1720, 8350},
		{"one direction's rule recreated alone", reading(90, 5, 0, 0), 1725, 8390},
	} {
		counters = step.read
		(&RelayService{}).CollectFrontTraffic()
		if row := frontRow(t, r.Id); row.TrafficIn != step.wantIn || row.TrafficOut != step.wantOut {
			t.Fatalf("%s: totals are %d in, %d out; want %d, %d", step.name, row.TrafficIn, row.TrafficOut, step.wantIn, step.wantOut)
		}
	}
}

// A disabled front counts nothing, so it is no reason to run iptables twice a minute.
func TestTheFirewallIsNotReadWithoutAnEnabledFront(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t, model.Relay{Name: "off", Address: "203.0.113.2", Tunnel: "backhaul", Role: model.RelayFront, Enabled: true})
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "off").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	stubFrontAcct(t, nil)
	(&RelayService{}).CollectFrontTraffic()
}

// The minute's reconcile counts every enabled front the panel dials, a delete takes
// its front out, and a panel going on standby counts none.
func TestTheFirewallFollowsTheEnabledFronts(t *testing.T) {
	setupRelayDB(t)
	t.Setenv("XUI_RELAY_DIR", t.TempDir())
	seedRelays(t,
		model.Relay{Name: "bh", Address: "203.0.113.9", TunnelPort: 3080, Transport: "ws", Tunnel: "backhaul", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "bh-off", Address: "203.0.113.10", TunnelPort: 3080, Transport: "tcp", Tunnel: "backhaul", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "bp", Address: "203.0.113.11", TunnelPort: 4080, Transport: "stealth", Tunnel: "backpack", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "nl", Address: "198.51.100.7", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
	)
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "bh-off").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	var rows []model.Relay
	database.GetDB().Find(&rows)
	for _, r := range rows {
		ids[r.Name] = r.Id
	}
	fits := stubFrontAcct(t, nil)
	last := func(step string, want []tunnel.Pair) {
		t.Helper()
		if len(*fits) == 0 {
			t.Fatalf("%s: the firewall was never fitted", step)
		}
		if got := (*fits)[len(*fits)-1]; len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Fatalf("%s: the firewall counts %+v; want %+v", step, got, want)
		}
	}
	bp := tunnel.Pair{Id: ids["bp"], FrontAddress: "203.0.113.11", TunnelPort: 4080, Transport: "stealth", Tool: tunnel.ToolBackPack}

	s := &RelayService{}
	s.Reconcile()
	last("the reconcile", []tunnel.Pair{
		{Id: ids["bh"], FrontAddress: "203.0.113.9", TunnelPort: 3080, Transport: "ws", Tool: tunnel.ToolBackhaul}, bp,
	})
	if err := s.Delete(ids["bh"]); err != nil {
		t.Fatal(err)
	}
	last("the delete", []tunnel.Pair{bp})

	global.SetStandby(true)
	t.Cleanup(func() { global.SetStandby(false) })
	s.StandDown()
	last("going on standby", nil)
}

// BackPack's own counters carry TCP only, so a front's UDP (Hysteria, OpenVPN on UDP,
// WireGuard) never showed in its usage: 4.75 MB of it crossed the tunnel and the counters did
// not move. The firewall counts the wire, so its readings are the totals and the client's own
// counters, moving too, add nothing on top.
func TestABackPackFrontIsCountedFromTheFirewallSoItsUDPCounts(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	var counters map[string]tunnel.LegMetrics
	stubFrontAcct(t, func() map[int]map[string]tunnel.LegMetrics {
		return map[int]map[string]tunnel.LegMetrics{r.Id: counters}
	})
	taken := time.Now().Add(-time.Minute)
	reading := func(out, in int64) map[string]tunnel.LegMetrics {
		taken = taken.Add(30 * time.Second)
		return map[string]tunnel.LegMetrics{
			key + ":out": {Taken: taken, BytesOut: out}, key + ":in": {Taken: taken, BytesIn: in},
		}
	}
	writeLegSnapshot(t, key, taken, 9_000, 77_000)
	counters = reading(6_000_000, 40_000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 40_000, 6_000_000)

	writeLegSnapshot(t, key, taken.Add(30*time.Second), 19_000, 99_000)
	counters = reading(6_500_000, 45_000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 45_000, 6_500_000)
}

// BackPack's own counters stand in where the firewall has never counted the front (no
// iptables, a front named by a host name); once it has, a minute it cannot be read adds nothing,
// and the client's counters, which began long before, are not counted in full on top.
func TestBackPacksOwnCountersStandInOnlyUntilTheFirewallHasCounted(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	var counters map[string]tunnel.LegMetrics
	stubFrontAcct(t, func() map[int]map[string]tunnel.LegMetrics {
		return map[int]map[string]tunnel.LegMetrics{r.Id: counters}
	})
	taken := time.Now().Add(-time.Minute)
	writeLegSnapshot(t, key, taken, 1000, 5000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1000, 5000)

	taken = taken.Add(30 * time.Second)
	counters = map[string]tunnel.LegMetrics{
		key + ":out": {Taken: taken, BytesOut: 700}, key + ":in": {Taken: taken, BytesIn: 70},
	}
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1070, 5700)

	counters = nil
	writeLegSnapshot(t, key, taken.Add(30*time.Second), 99_000, 99_000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1070, 5700)
}

// The ports the panel's own inbounds bind on UDP alone: a TCP knock on one is refused here by
// design. A port any enabled inbound also binds on TCP is not one, nor is a disabled inbound's.
func TestAFrontKnowsWhichForwardedPortsAreUDPOnly(t *testing.T) {
	setupRelayDB(t)
	for _, ib := range []*model.Inbound{
		{Tag: "ovpn-udp", Enable: true, Port: 1194, Protocol: model.OpenVPN, Settings: `{"useTcp":false}`},
		{Tag: "ovpn-tcp", Enable: true, Port: 1196, Protocol: model.OpenVPN, Settings: `{"useTcp":true}`},
		{Tag: "hy", Enable: true, Port: 2576, Protocol: model.Hysteria},
		{Tag: "hy-off", Enable: false, Port: 25000, Protocol: model.Hysteria},
		{Tag: "vless", Enable: true, Port: 41330, Protocol: model.VLESS},
		{Tag: "cisco", Enable: true, Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":443}`},
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&RelayService{}).frontUDPOnlyPorts()
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1194, 2576}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UDP-only ports %v; want %v", got, want)
	}
}
