package openvpn

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// fakeRouteHost stands in for the host under the route code: it keeps the NAT rules
// a real host would, refuses a route onto a device that is not there, and lists every
// command and steer in order. Nothing here runs a command.
type fakeRouteHost struct {
	events  []string
	nat     map[string]bool
	steered []relay.PoolExit
}

func fakeRoutes(t *testing.T) *fakeRouteHost {
	t.Helper()
	f := &fakeRouteHost{nat: map[string]bool{}}
	savedLinux, savedRun, savedOut := onLinux, run, runOut
	savedSteer, savedReassert, savedSNAT, savedUnSNAT := steerPool, reassertPool, ensureExitSNAT, removeExitSNAT
	savedRelease, savedGuards := releasePool, reassertGuards
	t.Cleanup(func() {
		onLinux, run, runOut = savedLinux, savedRun, savedOut
		steerPool, reassertPool, ensureExitSNAT, removeExitSNAT = savedSteer, savedReassert, savedSNAT, savedUnSNAT
		releasePool, reassertGuards = savedRelease, savedGuards
	})
	onLinux = true
	run = func(name string, args ...string) error {
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
		f.events = append(f.events, name+" "+strings.Join(args, " "))
		// Every exit's device is missing here, as wgr9 is before relay.Up brings it up.
		if name == "ip" && args[0] == "route" && args[1] == "replace" && slices.Contains(args, "dev") {
			return errors.New("Cannot find device")
		}
		return nil
	}
	runOut = func(string, ...string) (string, error) { return "", nil }
	steerPool = func(p relay.PoolExit) (bool, bool, error) {
		f.steered = append(f.steered, p)
		return false, true, nil
	}
	reassertPool = func(relay.PoolExit) (bool, bool, error) {
		t.Error("a start went through the periodic reassert")
		return false, false, nil
	}
	ensureExitSNAT = func(string, string) error { return nil }
	removeExitSNAT = func(string) {}
	releasePool = func(cidr string) error {
		f.events = append(f.events, "release "+cidr)
		return nil
	}
	reassertGuards = func() (bool, error) { return false, nil }
	return f
}

// A missing exit device used to return before the rule, with the NAT already gone, and
// the pool fell through to main; it is steered all the same, held at its blackhole.
func TestAnInboundWhoseExitIsMissingIsStillSteered(t *testing.T) {
	f := fakeRoutes(t)
	f.nat["10.1.3.0/24"] = true
	ensureExit("10.1.3.0/24", " wgr9 ", 7603)

	want := []relay.PoolExit{{CIDR: "10.1.3.0/24", Exit: "wgr9", Table: 7603}}
	if !slices.Equal(f.steered, want) {
		t.Errorf("steered %+v, want %+v (events %q)", f.steered, want, f.events)
	}
	if f.nat["10.1.3.0/24"] {
		t.Error("a pool with an exit is still masqueraded here")
	}
}

// The ten-second reassert walks every inbound. One with no exit is masqueraded
// here, and steering it would hold it at a blackhole: cut off.
func TestReassertSteersOnlyTheInboundsWithAnExit(t *testing.T) {
	fakeRoutes(t)
	var steered []relay.PoolExit
	reassertPool = func(p relay.PoolExit) (bool, bool, error) {
		steered = append(steered, p)
		return true, false, nil
	}

	m := &Manager{procs: map[int]*managed{
		1: {cidr: "10.1.1.0/24", table: 7601},
		2: {cidr: "10.1.2.0/24", table: 7602, exit: "psi-AT"},
	}}
	m.ReassertRoutes()

	want := []relay.PoolExit{{CIDR: "10.1.2.0/24", Exit: "psi-AT", Table: 7602}}
	if !slices.Equal(steered, want) {
		t.Fatalf("steered %+v, want %+v", steered, want)
	}
}

// Every ten seconds over every inbound: a line each time would bury the log. Only a
// pass that wrote something says so.
func TestReassertLogsOnlyWhatItChanged(t *testing.T) {
	fakeRoutes(t)
	const cidr = "10.1.77.0/24"
	m := &Manager{procs: map[int]*managed{1: {cidr: cidr, table: 7677, exit: "psi-AT"}}}
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
		{err: errors.New("table 7677 leaves through wgr5, not psi-AT")},
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

// An inbound whose exit is cleared, or that is removed, kept its Psiphon redirect and DNS
// rule: the next inbound handed its pool would leave through that country.
func TestAnInboundThatLeavesItsExitIsReleased(t *testing.T) {
	f := fakeRoutes(t)
	ensureExit("10.1.3.0/24", "", 7603)
	removeExit("10.1.4.0/24", 7604)
	for _, cidr := range []string{"10.1.3.0/24", "10.1.4.0/24"} {
		if !slices.Contains(f.events, "release "+cidr) {
			t.Errorf("events %q: %s was not released", f.events, cidr)
		}
	}
	if !f.nat["10.1.3.0/24"] {
		t.Error("the inbound left with no exit is not masqueraded here")
	}
}

// The pass also puts back the guards on the ports Xray takes redirected pools on, which
// a flushed iptables takes; without them those ports carry anyone into Psiphon.
func TestReassertPutsTheRedirectGuardsBack(t *testing.T) {
	fakeRoutes(t)
	guarded := func() int {
		return len(slices.DeleteFunc(logger.GetLogs(10000, "info"), func(line string) bool {
			return !strings.Contains(line, "openvpn: the Psiphon redirect ports are guarded again")
		}))
	}
	m := &Manager{procs: map[int]*managed{}}
	for _, wrote := range []bool{false, true} {
		asked := false
		reassertGuards = func() (bool, error) { asked = true; return wrote, nil }
		before := guarded()
		m.ReassertRoutes()
		if want := map[bool]int{false: 0, true: 1}[wrote]; !asked || guarded()-before != want {
			t.Errorf("wrote=%v: asked=%v, %d lines, want %d", wrote, asked, guarded()-before, want)
		}
	}
}

// 7600 + id%300 gave inbounds 300 apart one table, and with it one exit: each OpenVPN
// pool needs a table no other inbound, no cisco location and no Xray exit uses.
func TestEveryInboundGetsItsOwnRoutingTable(t *testing.T) {
	const ids = 4000
	seen := make(map[int]struct{})
	for slot := range ids * 16 {
		seen[relay.CiscoPoolTable(slot)] = struct{}{}
	}
	for id := range ids {
		table := routeTableFor(id)
		if _, clash := seen[table]; clash {
			t.Fatalf("inbound %d reuses routing table %d", id, table)
		}
		if table < 256 || (table > 60000 && table <= 60254) {
			t.Fatalf("routing table %d is Linux's own, a hand-written one's or an Xray exit's", table)
		}
		seen[table] = struct{}{}
	}
}
