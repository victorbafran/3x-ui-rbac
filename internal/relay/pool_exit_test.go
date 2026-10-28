package relay

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakePoolHost answers the two reads with what a box would print and records every
// write; a write listed in fail is refused, the way a vanished device refuses it.
type fakePoolHost struct {
	table, rules string
	// unread is what `ip route show table` prints when it fails, with exit status 2.
	unread string
	up     bool
	fail   map[string]bool
	writes []string
}

func (f *fakePoolHost) host() poolHost {
	return poolHost{
		ip: func(args ...string) (string, error) {
			cmd := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(cmd, "route show table ") && f.unread != "":
				return f.unread, errors.New("exit status 2")
			case strings.HasPrefix(cmd, "route show table "):
				return f.table, nil
			case cmd == "rule show":
				return f.rules, nil
			}
			f.writes = append(f.writes, cmd)
			if f.fail[cmd] {
				return "Cannot find device", errors.New("exit status 1")
			}
			return "", nil
		},
		deviceUp: func(string) bool { return f.up },
	}
}

const (
	poolRulesShown = "0:\tfrom all lookup local\n7003:\tfrom 10.248.48.0/24 lookup 7003\n32766:\tfrom all lookup main\n"
	poolHeldShown  = "blackhole default metric 4000 \n"
)

var atPool = PoolExit{CIDR: "10.248.48.0/24", Exit: "psi-AT", Table: 7003}

// Measured before this: a missing psi-AT returned before the rule, NAT already gone,
// and the pool fell through to main, out of this server. It must be held instead.
func TestAMissingDeviceStillGetsItsBlackholeAndRule(t *testing.T) {
	f := &fakePoolHost{rules: "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n"}
	through, wrote, err := steerPool(atPool, f.host(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route replace blackhole default table 7003 metric 4000",
		"rule add from 10.248.48.0/24 lookup 7003 priority 7003",
	}
	if !slices.Equal(f.writes, want) {
		t.Fatalf("writes %q, want %q", f.writes, want)
	}
	if through || !wrote {
		t.Errorf("through=%v wrote=%v, want a held pool that was written", through, wrote)
	}
}

// Every ten seconds this runs over every country: a table already right is read and
// left alone, and one whose device came back gets only its route.
func TestReassertWritesOnlyWhatIsMissing(t *testing.T) {
	t.Run("the device is back", func(t *testing.T) {
		f := &fakePoolHost{table: poolHeldShown, rules: poolRulesShown, up: true}
		through, wrote, err := steerPool(atPool, f.host(), false)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"route replace default dev psi-AT table 7003"}; !slices.Equal(f.writes, want) {
			t.Fatalf("writes %q, want %q", f.writes, want)
		}
		if !through || !wrote {
			t.Errorf("through=%v wrote=%v, want the pool back on its exit", through, wrote)
		}
	})
	t.Run("nothing is missing", func(t *testing.T) {
		f := &fakePoolHost{table: "default dev psi-AT scope link \n" + poolHeldShown, rules: poolRulesShown, up: true}
		through, wrote, err := steerPool(atPool, f.host(), false)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.writes) != 0 || wrote || !through {
			t.Errorf("writes %q wrote=%v through=%v, want nothing written on a table already right", f.writes, wrote, through)
		}
	})
}

// The device can go between the read and the write, as Xray restarting does; the
// rule still goes in behind the blackhole rather than the pool being left on main.
func TestADeviceThatVanishesMidWriteLeavesThePoolHeld(t *testing.T) {
	f := &fakePoolHost{up: true, fail: map[string]bool{"route replace default dev psi-AT table 7003": true}}
	through, _, err := steerPool(atPool, f.host(), false)
	if err != nil {
		t.Fatalf("a vanished device failed the whole pool: %v", err)
	}
	if through {
		t.Error("a pool whose route was refused reads as leaving through its exit")
	}
	if last := f.writes[len(f.writes)-1]; last != "rule add from 10.248.48.0/24 lookup 7003 priority 7003" {
		t.Errorf("last write %q, want the rule", last)
	}
}

// A country moved from a relay to Psiphon before its TUN is up would otherwise keep
// leaving through the relay, from the wrong country.
func TestARouteToThePreviousExitIsTakenOut(t *testing.T) {
	f := &fakePoolHost{table: "default dev wgr9 scope link \n" + poolHeldShown, rules: poolRulesShown}
	if _, _, err := steerPool(atPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"route del default dev wgr9 table 7003"}; !slices.Equal(f.writes, want) {
		t.Fatalf("writes %q, want %q", f.writes, want)
	}
}

// `ip rule show` prints the priority first and the table by name when rt_tables has
// one; a rule into another table of the same pool is not this one.
func TestPoolRuleInReadsRealRuleOutput(t *testing.T) {
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"7003:\tfrom 10.248.48.0/24 lookup 7003", true},
		{"7003:\tfrom 10.248.48.0/24 lookup 70031", false},
		{"7003:\tfrom 10.248.49.0/24 lookup 7003", false},
		{"7003:\tfrom 10.248.48.0/24 lookup austria", true},
		{"7004:\tfrom 10.248.48.0/24 lookup austria", false},
	} {
		if got := PoolRuleIn(c.out, "10.248.48.0/24", 7003); got != c.want {
			t.Errorf("PoolRuleIn(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

// Measured on the test panel (kernel 6.8, iproute2 6.1): a table nothing has written
// since boot cannot even be listed. Every table is such a table after a reboot, and
// failing on it left every pool with no rule, no route and no NAT, for good.
func TestATableNothingHasWrittenYetIsCreated(t *testing.T) {
	const missing = "Error: ipv4: FIB table does not exist.\nDump terminated\n"
	for _, up := range []bool{false, true} {
		f := &fakePoolHost{unread: missing, rules: "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n", up: up}
		through, _, err := steerPool(atPool, f.host(), false)
		if err != nil {
			t.Fatalf("up=%v: %v", up, err)
		}
		want := []string{"route replace blackhole default table 7003 metric 4000"}
		if up {
			want = append(want, "route replace default dev psi-AT table 7003")
		}
		want = append(want, "rule add from 10.248.48.0/24 lookup 7003 priority 7003")
		if !slices.Equal(f.writes, want) || through != up {
			t.Errorf("up=%v: writes %q through=%v, want %q through=%v", up, f.writes, through, want, up)
		}
	}

	// Any other failure to read is still one: nothing is written on a guess.
	f := &fakePoolHost{unread: "Cannot open netlink socket: Permission denied\n"}
	if _, _, err := steerPool(atPool, f.host(), false); err == nil || len(f.writes) != 0 {
		t.Errorf("an unreadable table: err=%v writes %q, want an error and nothing written", err, f.writes)
	}
}

// A cisco location and an OpenVPN inbound can be numbered into one table. A start
// still writes its own exit there, as it always did; the ten-second pass must not,
// or the two pools take the table from each other every tick and switch country.
func TestAReassertLeavesAnotherPoolsExitAlone(t *testing.T) {
	shared := poolRulesShown + "7003:\tfrom 10.1.3.0/24 lookup 7003\n"
	theirs := "default dev wgr9 scope link \n" + poolHeldShown

	f := &fakePoolHost{table: theirs, rules: shared, up: true}
	through, wrote, err := steerPool(atPool, f.host(), true)
	if len(f.writes) != 0 || wrote || through {
		t.Errorf("reassert wrote %q (through=%v), want the other pool's exit left where it is", f.writes, through)
	}
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Errorf("reassert err = %v, want the collision named", err)
	}

	f = &fakePoolHost{table: theirs, rules: shared, up: true}
	if _, _, err := steerPool(atPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"route del default dev wgr9 table 7003", "route replace default dev psi-AT table 7003"}
	if !slices.Equal(f.writes, want) {
		t.Errorf("a start wrote %q, want %q", f.writes, want)
	}

	// With nobody's exit in the table the pass puts its own back, as before.
	f = &fakePoolHost{table: poolHeldShown, rules: shared, up: true}
	if through, _, err := steerPool(atPool, f.host(), true); err != nil || !through {
		t.Errorf("an empty shared table: through=%v err=%v, want the pool's own exit put back", through, err)
	}
}

// The blackhole and the rule are what hold a pool closed; a refused write of either
// is an error, never a pool that reads as steered.
func TestAWriteThatMustSucceedFailsTheSteer(t *testing.T) {
	for _, cmd := range []string{
		"route replace blackhole default table 7003 metric 4000",
		"rule add from 10.248.48.0/24 lookup 7003 priority 7003",
	} {
		f := &fakePoolHost{fail: map[string]bool{cmd: true}}
		if _, _, err := steerPool(atPool, f.host(), false); err == nil || !strings.Contains(err.Error(), cmd) {
			t.Errorf("%q refused: err=%v, want it named", cmd, err)
		}
	}
	f := &fakePoolHost{fail: map[string]bool{"route replace blackhole default table 7003 metric 4000": true}}
	steerPool(atPool, f.host(), false)
	if slices.Contains(f.writes, "rule add from 10.248.48.0/24 lookup 7003 priority 7003") {
		t.Error("the rule went in over a table with no blackhole, which hands the pool on to main")
	}
}

// Only the pool's own hold counts: a blackhole someone else put there at another
// metric can sit in front of the exit's route, or be taken away with its owner.
func TestOnlyTheHoldsOwnMetricCounts(t *testing.T) {
	f := &fakePoolHost{table: "blackhole default metric 100 \n", rules: poolRulesShown}
	if _, _, err := steerPool(atPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"route replace blackhole default table 7003 metric 4000"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q", f.writes, want)
	}
}
