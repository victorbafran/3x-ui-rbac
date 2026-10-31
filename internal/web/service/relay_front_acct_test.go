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
			t.Error("the firewall was read with no enabled Backhaul front to count")
			return nil, nil
		}
		counters, named := read(), map[int]map[string]tunnel.LegMetrics{}
		for _, f := range fronts {
			if c, ok := counters[f.Id]; ok && f.Tool == tunnel.ToolBackhaul {
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

// BackPack counts itself and a disabled front counts nothing, so neither is a
// reason to run iptables twice a minute.
func TestTheFirewallIsNotReadWithoutAnEnabledBackhaulFront(t *testing.T) {
	seedBackPackFront(t)
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
