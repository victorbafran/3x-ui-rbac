package ocserv

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// fakeRoutesHost stands in for the host under the route code: it keeps the NAT
// rules and ip rules a real host would, refuses a route onto a device that is not
// there, and lists every write in order. Nothing here runs a command.
type fakeRoutesHost struct {
	events []string
	nat    map[string]bool
	rules  map[string]bool // "cidr table"
}

func fakeRoutes(t *testing.T) *fakeRoutesHost {
	t.Helper()
	f := &fakeRoutesHost{nat: map[string]bool{}, rules: map[string]bool{}}
	savedLinux, savedRun, savedOut := onLinux, run, runOut
	savedSteer, savedReassert, savedSNAT, savedUnSNAT := steerPool, reassertPool, ensureExitSNAT, removeExitSNAT
	t.Cleanup(func() {
		onLinux, run, runOut = savedLinux, savedRun, savedOut
		steerPool, reassertPool, ensureExitSNAT, removeExitSNAT = savedSteer, savedReassert, savedSNAT, savedUnSNAT
	})
	onLinux = true
	run = func(name string, args ...string) error {
		cmd := name + " " + strings.Join(args, " ")
		if name == "iptables" {
			cidr := args[slices.Index(args, "-s")+1]
			switch args[2] {
			case "-C":
				if !f.nat[cidr] {
					return errors.New("exit status 1")
				}
				return nil
			case "-A":
				f.nat[cidr] = true
			case "-D":
				delete(f.nat, cidr)
			}
		}
		if name == "ip" && args[0] == "rule" && args[1] == "del" {
			delete(f.rules, args[3]+" "+args[5])
		}
		f.events = append(f.events, cmd)
		// Every exit's device is missing here, as psi-AT is before Xray brings it up.
		if name == "ip" && args[0] == "route" && args[1] == "replace" && slices.Contains(args, "dev") {
			return errors.New("Cannot find device")
		}
		return nil
	}
	runOut = func(name string, args ...string) (string, error) {
		var out strings.Builder
		for rule := range f.rules {
			cidr, table, _ := strings.Cut(rule, " ")
			fmt.Fprintf(&out, "%s:\tfrom %s lookup %s\n", table, cidr, table)
		}
		return out.String(), nil
	}
	steerPool = func(p relay.PoolExit) (bool, bool, error) {
		f.events = append(f.events, fmt.Sprintf("steer %s %s %d", p.CIDR, p.Exit, p.Table))
		f.rules[p.CIDR+" "+strconv.Itoa(p.Table)] = true
		return false, true, nil
	}
	reassertPool = func(relay.PoolExit) (bool, bool, error) {
		t.Error("a start went through the periodic reassert")
		return false, false, nil
	}
	ensureExitSNAT = func(cidr, exit string) error {
		f.events = append(f.events, "snat "+cidr+" "+exit)
		return nil
	}
	removeExitSNAT = func(cidr string) { f.events = append(f.events, "unsnat "+cidr) }
	return f
}

// last is the index of the last event that is want, -1 for none.
func (f *fakeRoutesHost) last(want string) int {
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i] == want {
			return i
		}
	}
	return -1
}

// A missing psi-AT used to return before the rule, with the NAT already gone, and
// the pool fell through to main and out of this server. It is steered all the same,
// so its table holds it at the blackhole until the device is there.
func TestALocationWhoseExitIsMissingIsStillSteered(t *testing.T) {
	f := fakeRoutes(t)
	f.nat["10.248.48.0/24"] = true
	ensureLocationRoute(locationRoute{CIDR: "10.248.48.0/24", Exit: "psi-AT", Table: 7003})

	if !slices.Contains(f.events, "steer 10.248.48.0/24 psi-AT 7003") {
		t.Errorf("events %q: the pool was never steered into its table", f.events)
	}
	if f.nat["10.248.48.0/24"] {
		t.Error("a pool with an exit is still masqueraded here")
	}
}

// Two countries with typed networks trade places. Each stale pair was taken away
// whole: its table flushed though the other country had just been steered into
// it, and its pool's NAT and rewrite removed though that pool was still served.
func TestTwoCountriesTradingTablesKeepWhatEachNowHolds(t *testing.T) {
	f := fakeRoutes(t)
	f.nat["10.91.0.0/24"] = true
	f.rules["10.90.0.0/24 7000"] = true
	previous := []locationRoute{
		{CIDR: "10.90.0.0/24", Exit: "wgr9", Table: 7000},
		{CIDR: "10.91.0.0/24", Table: 7001},
	}
	wanted := []locationRoute{
		{CIDR: "10.91.0.0/24", Table: 7000},
		{CIDR: "10.90.0.0/24", Exit: "wgr9", Table: 7001},
	}
	syncLocationRoutes(previous, wanted)

	if !f.nat["10.91.0.0/24"] {
		t.Error("the country that stays here lost its NAT: its customers reach nothing")
	}
	if steer := f.last("steer 10.90.0.0/24 wgr9 7001"); steer < 0 || f.last("ip route flush table 7001") > steer {
		t.Errorf("events %q: table 7001 was flushed after the country moved into it", f.events)
	}
	if snat := f.last("snat 10.90.0.0/24 wgr9"); snat < 0 || f.last("unsnat 10.90.0.0/24") > snat {
		t.Errorf("events %q: the moved country's rewrite was taken away after it was written", f.events)
	}
	if want := map[string]bool{"10.90.0.0/24 7001": true}; !maps.Equal(f.rules, want) {
		t.Errorf("rules %v, want only %v", f.rules, want)
	}
}

// The ten-second reassert walks every country of every instance. One with no exit
// is masqueraded here, and steering it would hold it at a blackhole: cut off.
func TestReassertSteersOnlyTheCountriesWithAnExit(t *testing.T) {
	fakeRoutes(t)
	var steered []string
	reassertPool = func(p relay.PoolExit) (bool, bool, error) {
		steered = append(steered, p.Exit+" "+p.CIDR)
		return true, false, nil
	}

	m := &Manager{procs: map[int]*managed{
		1: {routes: []locationRoute{
			{CIDR: "10.248.16.0/24", Table: 7016},
			{CIDR: "10.248.17.0/24", Exit: "psi-AT", Table: 7017},
		}},
		2: {routes: []locationRoute{{CIDR: "10.248.32.0/24", Exit: " wgr9 ", Table: 7032}}},
	}}
	m.ReassertRoutes()

	slices.Sort(steered)
	if want := []string{"psi-AT 10.248.17.0/24", "wgr9 10.248.32.0/24"}; !slices.Equal(steered, want) {
		t.Fatalf("steered %q, want %q", steered, want)
	}
}

// Every ten seconds over every country: a line each time would bury the log. Only a
// pass that wrote something says so.
func TestReassertLogsOnlyWhatItChanged(t *testing.T) {
	fakeRoutes(t)
	const cidr = "10.251.77.0/24"
	m := &Manager{procs: map[int]*managed{1: {routes: []locationRoute{{CIDR: cidr, Exit: "psi-AT", Table: 7077}}}}}
	lines := func() int {
		n := 0
		for _, line := range logger.GetLogs(10000, "info") {
			if strings.Contains(line, cidr) {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		through, wrote bool
		err            error
		logs           int
	}{
		{through: true},
		{err: errors.New("table 7077 leaves through wgr9, another pool's exit")},
		{through: true, wrote: true, logs: 1},
		{wrote: true, logs: 1},
	} {
		before := lines()
		reassertPool = func(relay.PoolExit) (bool, bool, error) { return c.through, c.wrote, c.err }
		m.ReassertRoutes()
		if got := lines() - before; got != c.logs {
			t.Errorf("through=%v wrote=%v err=%v: %d lines at info, want %d", c.through, c.wrote, c.err, got, c.logs)
		}
	}
}
