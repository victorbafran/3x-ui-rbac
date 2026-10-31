package relay

import (
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakePoolHost answers the reads with what a box would print and records every write;
// a write listed in fail is refused, the way a vanished device refuses it.
type fakePoolHost struct {
	table, rules string
	// unread is what `ip route show table` prints when it fails, with exit status 2.
	unread string
	up     bool
	// nat is `iptables -t nat -S PREROUTING`; dnsNAT answers the DNS rewrite's -C. natUnread
	// is a box without iptables, natBusy one whose lock another writer held too long.
	nat       string
	dnsNAT    bool
	natUnread bool
	natBusy   bool
	natCalls  int
	fail      map[string]bool
	writes    []string
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
		iptables: func(args ...string) (string, error) {
			f.natCalls++
			switch {
			case slices.Contains(args, "-S") && f.natUnread:
				return "", &exec.Error{Name: "iptables", Err: exec.ErrNotFound}
			case slices.Contains(args, "-S") && f.natBusy:
				return "Another app is currently holding the xtables lock. Stopped waiting after 5s.", errors.New("exit status 4")
			case slices.Contains(args, "-S"):
				return f.nat, nil
			case slices.Contains(args, "-C") && f.dnsNAT:
				return "", nil
			case slices.Contains(args, "-C"):
				return "iptables: Bad rule (does a matching rule exist in that chain?).", errors.New("exit status 1")
			}
			cmd := "iptables " + strings.Join(args, " ")
			f.writes = append(f.writes, cmd)
			if f.fail[cmd] {
				return "iptables: No chain/target/match by that name.", errors.New("exit status 1")
			}
			return "", nil
		},
		deviceUp: func(string) bool { return f.up },
	}
}

const (
	poolRulesShown = "0:\tfrom all lookup local\n10012:\tfrom 10.248.48.0/24 to 10.248.48.0/24 iif lo lookup main\n" +
		"10013:\tfrom 10.248.48.0/24 lookup 1000006\n32766:\tfrom all lookup main\n"
	localRuleAdd   = "rule add from 10.248.48.0/24 to 10.248.48.0/24 iif lo lookup main priority 10012"
	bareRulesShown = "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n"
	poolHeldShown  = "blackhole default metric 4000 \n"
	wgr9Shown      = "default dev wgr9 scope link \n" + poolHeldShown
)

var wgPool = PoolExit{CIDR: "10.248.48.0/24", Exit: "wgr9", Table: 1_000_006}

// Measured before this: a missing exit device returned before the rule, NAT already
// gone, and the pool fell through to main, out of this server. It must be held instead.
func TestAMissingDeviceStillGetsItsBlackholeAndRule(t *testing.T) {
	f := &fakePoolHost{rules: bareRulesShown}
	through, wrote, err := steerPool(wgPool, f.host(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route replace blackhole default table 1000006 metric 4000",
		"rule add from 10.248.48.0/24 lookup 1000006 priority 10013",
		localRuleAdd,
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
		through, wrote, err := steerPool(wgPool, f.host(), true)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"route replace default dev wgr9 table 1000006"}; !slices.Equal(f.writes, want) {
			t.Fatalf("writes %q, want %q", f.writes, want)
		}
		if !through || !wrote {
			t.Errorf("through=%v wrote=%v, want the pool back on its exit", through, wrote)
		}
	})
	t.Run("nothing is missing", func(t *testing.T) {
		f := &fakePoolHost{table: wgr9Shown, rules: poolRulesShown, up: true}
		through, wrote, err := steerPool(wgPool, f.host(), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.writes) != 0 || wrote || !through {
			t.Errorf("writes %q wrote=%v through=%v, want nothing written on a table already right", f.writes, wrote, through)
		}
	})
}

// The device can go between the read and the write, as wg-quick restarting does; the
// rule still goes in behind the blackhole rather than the pool being left on main.
func TestADeviceThatVanishesMidWriteLeavesThePoolHeld(t *testing.T) {
	f := &fakePoolHost{rules: bareRulesShown, up: true, fail: map[string]bool{"route replace default dev wgr9 table 1000006": true}}
	through, _, err := steerPool(wgPool, f.host(), false)
	if err != nil {
		t.Fatalf("a vanished device failed the whole pool: %v", err)
	}
	if through {
		t.Error("a pool whose route was refused reads as leaving through its exit")
	}
	if !slices.Contains(f.writes, "rule add from 10.248.48.0/24 lookup 1000006 priority 10013") {
		t.Errorf("writes %q, want the rule", f.writes)
	}
}

// A country moved to another relay before that relay's device is up would otherwise
// keep leaving through the old one, from the wrong country.
func TestARouteToThePreviousExitIsTakenOut(t *testing.T) {
	f := &fakePoolHost{table: "default dev wgr5 scope link \n" + poolHeldShown, rules: poolRulesShown}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"route del default dev wgr5 table 1000006"}; !slices.Equal(f.writes, want) {
		t.Fatalf("writes %q, want %q", f.writes, want)
	}
}

// `ip rule show` prints the priority first and the table by name when rt_tables has
// one; a rule into another table, of another pool or with more selectors is not this one.
func TestPoolRuleInReadsRealRuleOutput(t *testing.T) {
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"10013:\tfrom 10.248.48.0/24 lookup 1000006", true},
		{"10013:\tfrom 10.248.48.0/24 lookup 10000061", false},
		{"10013:\tfrom 10.248.49.0/24 lookup 1000006", false},
		{"10013:\tfrom 10.248.48.0/24 lookup austria", true},
		{"10015:\tfrom 10.248.48.0/24 lookup austria", false},
		{"10013:\tfrom 10.248.48.0/24 ipproto udp dport 53 lookup 1000006", false},
	} {
		if got := PoolRuleIn(c.out, "10.248.48.0/24", 1_000_006); got != c.want {
			t.Errorf("PoolRuleIn(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

// Measured on the test panel (kernel 6.8, iproute2 6.1): a table nothing has written
// since boot cannot even be listed, and failing on it left every pool open for good.
func TestATableNothingHasWrittenYetIsCreated(t *testing.T) {
	const missing = "Error: ipv4: FIB table does not exist.\nDump terminated\n"
	for _, up := range []bool{false, true} {
		f := &fakePoolHost{unread: missing, rules: bareRulesShown, up: up}
		through, _, err := steerPool(wgPool, f.host(), false)
		if err != nil {
			t.Fatalf("up=%v: %v", up, err)
		}
		want := []string{"route replace blackhole default table 1000006 metric 4000"}
		if up {
			want = append(want, "route replace default dev wgr9 table 1000006")
		}
		want = append(want, "rule add from 10.248.48.0/24 lookup 1000006 priority 10013", localRuleAdd)
		if !slices.Equal(f.writes, want) || through != up {
			t.Errorf("up=%v: writes %q through=%v, want %q through=%v", up, f.writes, through, want, up)
		}
	}

	// Any other failure to read is still one: nothing is written on a guess.
	f := &fakePoolHost{unread: "Cannot open netlink socket: Permission denied\n"}
	if _, _, err := steerPool(wgPool, f.host(), false); err == nil || len(f.writes) != 0 {
		t.Errorf("an unreadable table: err=%v writes %q, want an error and nothing written", err, f.writes)
	}
}

// Another writer's exit in a pool's table: a start writes the pool's own exit there, as
// it always did, but the ten-second pass must not, or the two take it every tick.
func TestAReassertLeavesAnotherWritersExitAlone(t *testing.T) {
	theirs := "default dev wgr5 scope link \n" + poolHeldShown

	f := &fakePoolHost{table: theirs, rules: poolRulesShown, up: true}
	through, wrote, err := steerPool(wgPool, f.host(), true)
	if len(f.writes) != 0 || wrote || through {
		t.Errorf("reassert wrote %q (through=%v), want the other exit left where it is", f.writes, through)
	}
	if err == nil || !strings.Contains(err.Error(), "leaves through wgr5, not wgr9") {
		t.Errorf("reassert err = %v, want the other exit named", err)
	}

	f = &fakePoolHost{table: theirs, rules: poolRulesShown, up: true}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"route del default dev wgr5 table 1000006", "route replace default dev wgr9 table 1000006"}
	if !slices.Equal(f.writes, want) {
		t.Errorf("a start wrote %q, want %q", f.writes, want)
	}

	// With nobody's exit in the table the pass puts its own back, as before.
	f = &fakePoolHost{table: poolHeldShown, rules: poolRulesShown, up: true}
	if through, _, err := steerPool(wgPool, f.host(), true); err != nil || !through {
		t.Errorf("an empty table: through=%v err=%v, want the pool's own exit put back", through, err)
	}
}

// The blackhole and the rule are what hold a pool closed; a refused write of either
// is an error, never a pool that reads as steered.
func TestAWriteThatMustSucceedFailsTheSteer(t *testing.T) {
	for _, cmd := range []string{
		"route replace blackhole default table 1000006 metric 4000",
		"rule add from 10.248.48.0/24 lookup 1000006 priority 10013",
	} {
		f := &fakePoolHost{rules: bareRulesShown, fail: map[string]bool{cmd: true}}
		if _, _, err := steerPool(wgPool, f.host(), false); err == nil || !strings.Contains(err.Error(), cmd) {
			t.Errorf("%q refused: err=%v, want it named", cmd, err)
		}
	}
	f := &fakePoolHost{rules: bareRulesShown, fail: map[string]bool{"route replace blackhole default table 1000006 metric 4000": true}}
	_, _, _ = steerPool(wgPool, f.host(), false)
	if slices.Contains(f.writes, "rule add from 10.248.48.0/24 lookup 1000006 priority 10013") {
		t.Error("the rule went in over a table with no blackhole, which hands the pool on to main")
	}
}

// Only the pool's own hold counts: a blackhole someone else put there at another
// metric can sit in front of the exit's route, or be taken away with its owner.
func TestOnlyTheHoldsOwnMetricCounts(t *testing.T) {
	f := &fakePoolHost{table: "blackhole default metric 100 \n", rules: poolRulesShown}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"route replace blackhole default table 1000006 metric 4000"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q", f.writes, want)
	}
}

// cisco's 7000 + id*16 + index met OpenVPN's 7600 + id%300 at cisco inbound 38, and two
// pools in one table leave by one exit; a rule past main's 32766 leaves from here.
func TestNoTwoPoolsShareATable(t *testing.T) {
	owner := map[int]string{}
	check := func(table int, who string) {
		t.Helper()
		if prev, clash := owner[table]; clash {
			t.Fatalf("%s and %s share table %d", prev, who, table)
		}
		owner[table] = who
		if table < 256 || (table >= 60000 && table <= 60255) {
			t.Fatalf("%s takes table %d, Linux's own or an Xray exit's", who, table)
		}
		if prio := poolPriority(table); prio-1 <= 6254 || prio >= 32766 {
			t.Fatalf("%s's rules sit at %d and %d, outside 6255-32765", who, prio-1, prio)
		}
	}
	for slot := range 16 * 4000 {
		check(CiscoPoolTable(slot), "cisco slot "+strconv.Itoa(slot))
	}
	for id := range 64000 {
		check(OpenVPNPoolTable(id), "OpenVPN inbound "+strconv.Itoa(id))
	}
}

// After the upgrade a pool still has its rule into the old table, ahead of the new one,
// so it would keep the old table. A start takes that out, and nothing of another pool.
func TestAStartTakesTheOldNumberingOffItsOwnPool(t *testing.T) {
	rules := "0:\tfrom all lookup local\n" +
		"7003:\tfrom 10.248.48.0/24 lookup 7003\n" +
		"7608:\tfrom 10.248.48.0/24 lookup 7608\n" +
		"7608:\tfrom 10.1.8.0/24 lookup 7608\n" +
		"7609:\tfrom 10.1.9.0/24 lookup 7003\n" +
		"9000:\tfrom 10.248.48.0/24 fwmark 0x1 lookup 100\n" +
		"9001:\tfrom 10.248.48.0/24 lookup main\n" +
		"32766:\tfrom all lookup main\n"
	f := &fakePoolHost{table: poolHeldShown, rules: rules, up: true}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route replace default dev wgr9 table 1000006",
		"rule add from 10.248.48.0/24 lookup 1000006 priority 10013",
		localRuleAdd,
		"rule del priority 7003 from 10.248.48.0/24 lookup 7003",
		"rule del priority 7608 from 10.248.48.0/24 lookup 7608",
		"rule del priority 9001 from 10.248.48.0/24 lookup main",
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q: tables 7003 and 7608 are still another pool's, and main is never flushed", f.writes, want)
	}

	// Once nothing else looks a table up it goes with the rule.
	stale := "7003:\tfrom 10.248.48.0/24 lookup 7003\n" + poolRulesShown
	f = &fakePoolHost{table: wgr9Shown, rules: stale, up: true}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rule del priority 7003 from 10.248.48.0/24 lookup 7003", "route flush table 7003"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q", f.writes, want)
	}

	// main is never flushed, even on a box whose own rule into it is gone: that would
	// take this server's routes, and its SSH, with it.
	f = &fakePoolHost{table: wgr9Shown, rules: "9001:\tfrom 10.248.48.0/24 lookup main\n" + poolRulesShown, up: true}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rule del priority 9001 from 10.248.48.0/24 lookup main"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q", f.writes, want)
	}

	// The ten-second pass never sweeps: two pools given one network would take each
	// other's rule every tick.
	f = &fakePoolHost{table: wgr9Shown, rules: stale, up: true}
	if _, _, err := steerPool(wgPool, f.host(), true); err != nil || len(f.writes) != 0 {
		t.Errorf("reassert: writes %q err=%v, want nothing", f.writes, err)
	}
}

// A REDIRECT lands on the device's address, inside the pool: the pool rule took the box's
// SYN-ACK and the SYN's reverse-path check into the blackhole, and no TCP got through.
func TestWhatThisServerSendsItsOwnPoolLeavesByMain(t *testing.T) {
	for _, exit := range []string{"psi-AT", "wgr9"} {
		pool := PoolExit{CIDR: "10.1.3.0/24", Exit: exit, Table: OpenVPNPoolTable(3)}
		local := "rule add from 10.1.3.0/24 to 10.1.3.0/24 iif lo lookup main priority 10014"
		f := &fakePoolHost{rules: bareRulesShown, up: true}
		if _, _, err := steerPool(pool, f.host(), false); err != nil {
			t.Fatal(err)
		}
		if poolPriority(pool.Table) != 10015 || !slices.Contains(f.writes, local) {
			t.Errorf("%s start: writes %q, want %q ahead of the pool rule at %d", exit, f.writes, local, poolPriority(pool.Table))
		}

		// Something flushing the rules takes it too, and the pass puts it back.
		ruled := "10014:\tfrom 10.1.3.0/24 ipproto udp dport 53 lookup main\n10015:\tfrom 10.1.3.0/24 lookup 1000007\n"
		f = &fakePoolHost{table: poolHeldShown, rules: ruled, up: true}
		if _, _, err := steerPool(pool, f.host(), true); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(f.writes, local) {
			t.Errorf("%s pass: writes %q, want %q back", exit, f.writes, local)
		}
	}

	// Two pools given one network share the rule: a pass taking the other's to put its own
	// at its own priority would have the two trade it every ten seconds.
	shared := "10014:\tfrom 10.1.3.0/24 to 10.1.3.0/24 iif lo lookup main\n" +
		"10015:\tfrom 10.1.3.0/24 lookup 1000007\n10019:\tfrom 10.1.3.0/24 lookup 1000009\n"
	f := &fakePoolHost{table: wgr9Shown, rules: shared, up: true}
	other := PoolExit{CIDR: "10.1.3.0/24", Exit: "wgr9", Table: OpenVPNPoolTable(4)}
	if _, _, err := steerPool(other, f.host(), true); err != nil || len(f.writes) != 0 {
		t.Errorf("the other pool's pass: writes %q err=%v, want nothing", f.writes, err)
	}

	// A pool taken off its exit gives it up with the rest.
	f = &fakePoolHost{rules: "10014:\tfrom 10.1.3.0/24 to 10.1.3.0/24 iif lo lookup main\n" + bareRulesShown}
	if err := releasePool("10.1.3.0/24", f.host()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rule del priority 10014 from 10.1.3.0/24 to 10.1.3.0/24 iif lo lookup main"}; !slices.Equal(f.writes, want) {
		t.Errorf("release: writes %q, want %q", f.writes, want)
	}
}
