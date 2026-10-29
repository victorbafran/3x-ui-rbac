package relay

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

var atPool = PoolExit{CIDR: "10.248.48.0/24", Exit: "psi-AT", Table: 1_000_006}

const (
	psiRulesShown = "0:\tfrom all lookup local\n" +
		"10012:\tfrom 10.248.48.0/24 to 10.248.48.0/24 iif lo lookup main\n" +
		"10012:\tfrom 10.248.48.0/24 ipproto udp dport 53 lookup main\n" +
		"10013:\tfrom 10.248.48.0/24 lookup 1000006\n" +
		"32766:\tfrom all lookup main\n"
	dnsRuleAdd = "rule add from 10.248.48.0/24 ipproto udp dport 53 lookup main priority 10012"
	dnsNATAdd  = "iptables -w 5 -t nat -A POSTROUTING -s 10.248.48.0/24 -p udp --dport 53 -m comment --comment 3x-ui:psiphon -j MASQUERADE"
	dnsNATDel  = "iptables -w 5 -t nat -D POSTROUTING -s 10.248.48.0/24 -p udp --dport 53 -m comment --comment 3x-ui:psiphon -j MASQUERADE"
)

// redirectsAt stands in the ports the running config takes each exit's pools on.
func redirectsAt(t *testing.T, ports map[string]int) {
	t.Helper()
	r := &psiphonRedirects
	r.Lock()
	saved, savedStarting, savedFitted := r.ports, r.starting, r.fitted
	r.ports, r.starting = ports, nil
	r.Unlock()
	t.Cleanup(func() {
		r.Lock()
		r.ports, r.starting, r.fitted = saved, savedStarting, savedFitted
		r.Unlock()
	})
}

func redirectShown(cidr, port string) string {
	return `-A PREROUTING -s ` + cidr + ` -p tcp -m comment --comment "3x-ui:psiphon" -j REDIRECT --to-ports ` + port + "\n"
}

func redirectWrite(action, port string) string {
	return "iptables -w 5 -t nat " + action + " PREROUTING -s 10.248.48.0/24 -p tcp -m comment --comment 3x-ui:psiphon -j REDIRECT --to-ports " + port
}

// Xray's TUN fell to 0.55 MB/s at 2% loss where a REDIRECT kept 4.8 (panel box): the pool
// gets the redirect, its DNS out from here, no route into any device, and else the blackhole.
func TestAPsiphonPoolIsRedirectedIntoXray(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	f := &fakePoolHost{rules: bareRulesShown, up: true}
	through, wrote, err := steerPool(atPool, f.host(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route replace blackhole default table 1000006 metric 4000",
		"rule add from 10.248.48.0/24 lookup 1000006 priority 10013",
		localRuleAdd,
		dnsRuleAdd,
		dnsNATAdd,
		redirectWrite("-A", "11581"),
	}
	if !slices.Equal(f.writes, want) {
		t.Fatalf("writes %q\nwant %q", f.writes, want)
	}
	if !through || !wrote {
		t.Errorf("through=%v wrote=%v, want the pool on its country", through, wrote)
	}
}

// A country Xray does not take (turned off, or its port held by an inbound) has no port:
// a redirect left from before would send the pool to nothing, so it goes, and it is held.
func TestAPsiphonPoolXrayDoesNotTakeIsHeld(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-FR": 11582})
	f := &fakePoolHost{table: poolHeldShown, rules: psiRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true}
	through, _, err := steerPool(atPool, f.host(), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{redirectWrite("-D", "11581")}; !slices.Equal(f.writes, want) || through {
		t.Errorf("writes %q through=%v, want %q and the pool held", f.writes, through, want)
	}
}

// A redirect to another port goes only once the new one is in, so a moved port never
// leaves the pool's TCP on the blackhole between the two.
func TestAMovedPortIsRedirectedBeforeTheOldOneGoes(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11582})
	f := &fakePoolHost{table: poolHeldShown, rules: psiRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true}
	through, _, err := steerPool(atPool, f.host(), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{redirectWrite("-A", "11582"), redirectWrite("-D", "11581")}; !slices.Equal(f.writes, want) || !through {
		t.Errorf("writes %q through=%v, want %q", f.writes, through, want)
	}
}

// A box that ran the TUN build: the pool's rule sits ahead of the new one at 7003, into a
// table holding the blackhole alone. The start converges it to the redirect.
func TestAPoolTheTunBuildSteeredConverges(t *testing.T) {
	f := &fakePoolHost{
		unread: "Error: ipv4: FIB table does not exist.\nDump terminated\n",
		rules:  "0:\tfrom all lookup local\n7003:\tfrom 10.248.48.0/24 lookup 7003\n32766:\tfrom all lookup main\n",
	}
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	if _, _, err := steerPool(atPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route replace blackhole default table 1000006 metric 4000",
		"rule add from 10.248.48.0/24 lookup 1000006 priority 10013",
		localRuleAdd,
		dnsRuleAdd,
		"rule del priority 7003 from 10.248.48.0/24 lookup 7003",
		"route flush table 7003",
		dnsNATAdd,
		redirectWrite("-A", "11581"),
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q", f.writes, want)
	}
}

// A pool moved from a relay keeps no route in its table on either pass: anything not
// redirected would leave through that relay, from the wrong country.
func TestAPsiphonPoolsTableHoldsNoRoute(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	for _, reassert := range []bool{false, true} {
		f := &fakePoolHost{table: wgr9Shown, rules: psiRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true, up: true}
		through, _, err := steerPool(atPool, f.host(), reassert)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"route del default dev wgr9 table 1000006"}; !slices.Equal(f.writes, want) || !through {
			t.Errorf("reassert=%v: writes %q through=%v, want %q", reassert, f.writes, through, want)
		}
	}
}

// Something flushing iptables takes the redirect and the DNS rewrite: the ten-second
// pass puts them back, and writes nothing over a pool already right.
func TestReassertPutsBackWhatAFlushTook(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	f := &fakePoolHost{table: poolHeldShown, rules: psiRulesShown}
	through, wrote, err := steerPool(atPool, f.host(), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{dnsNATAdd, redirectWrite("-A", "11581")}; !slices.Equal(f.writes, want) || !through || !wrote {
		t.Errorf("writes %q through=%v wrote=%v, want %q", f.writes, through, wrote, want)
	}

	f = &fakePoolHost{table: poolHeldShown, rules: psiRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true}
	through, wrote, err = steerPool(atPool, f.host(), true)
	if err != nil || len(f.writes) != 0 || wrote || !through {
		t.Errorf("a pool already right: writes %q through=%v wrote=%v err=%v", f.writes, through, wrote, err)
	}
}

// A country moved from Psiphon to a relay kept its redirect, which sends its TCP into
// Psiphon still, and its DNS rule. The start takes them; the ten-second pass never reads.
func TestARelayPoolLosesWhatItsPsiphonExitLeft(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	f := &fakePoolHost{table: wgr9Shown, rules: psiRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true, up: true}
	if _, _, err := steerPool(wgPool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rule del priority 10012 from 10.248.48.0/24 ipproto udp dport 53 lookup main",
		dnsNATDel,
		redirectWrite("-D", "11581"),
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q", f.writes, want)
	}

	// Every relay pool goes through the pass every ten seconds, which leaves iptables alone.
	f = &fakePoolHost{table: wgr9Shown, rules: poolRulesShown, nat: redirectShown("10.248.48.0/24", "11581"), dnsNAT: true, up: true}
	if through, _, err := steerPool(wgPool, f.host(), true); err != nil || !through || f.natCalls != 0 {
		t.Errorf("the pass: through=%v err=%v, %d iptables calls, want none", through, err, f.natCalls)
	}

	// No iptables means nothing of a Psiphon exit can be there: a relay pool is not failed.
	for _, reassert := range []bool{false, true} {
		f = &fakePoolHost{table: wgr9Shown, rules: poolRulesShown, natUnread: true, up: true}
		if through, _, err := steerPool(wgPool, f.host(), reassert); err != nil || !through {
			t.Errorf("reassert=%v without iptables: through=%v err=%v", reassert, through, err)
		}
	}
}

// One read lost to another writer's iptables lock left a relay pool's leftover redirect for
// good, sending it through Psiphon: the start goes on, and the pass sweeps it once it reads.
func TestALeftoverAStartCouldNotReadIsSweptByThePass(t *testing.T) {
	t.Cleanup(func() { markUnswept(wgPool.CIDR, false) })
	f := &fakePoolHost{table: wgr9Shown, rules: poolRulesShown, natBusy: true, up: true}
	h := f.host()
	if through, _, err := steerPool(wgPool, h, false); err != nil || !through {
		t.Fatalf("the start: through=%v err=%v, want the relay's route standing", through, err)
	}
	if _, _, err := steerPool(wgPool, h, true); err == nil {
		t.Error("a pass that still cannot read reads as swept")
	}

	f.natBusy, f.nat, f.dnsNAT = false, redirectShown("10.248.48.0/24", "11581"), true
	if _, _, err := steerPool(wgPool, h, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{dnsNATDel, redirectWrite("-D", "11581")}; !slices.Equal(f.writes, want) {
		t.Errorf("the pass wrote %q, want %q", f.writes, want)
	}
	f.natCalls = 0
	if _, _, err := steerPool(wgPool, h, true); err != nil || f.natCalls != 0 {
		t.Errorf("once swept: %d iptables calls err=%v, want none", f.natCalls, err)
	}

	// A release that could not read says so, and leaves the pool to the next sweep.
	f = &fakePoolHost{rules: bareRulesShown, natBusy: true}
	if err := releasePool(wgPool.CIDR, f.host()); err == nil || !strings.Contains(err.Error(), "xtables lock") {
		t.Errorf("a release that could not read: %v", err)
	}
	if !natUnswept(wgPool.CIDR) {
		t.Error("a release that could not read left nothing to sweep")
	}
}

// The blackhole and the rule go in before any iptables write, so a refused redirect, or
// iptables that cannot be read, leaves the pool held rather than open.
func TestAnIptablesFailureLeavesThePoolHeld(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	f := &fakePoolHost{rules: bareRulesShown, fail: map[string]bool{redirectWrite("-A", "11581"): true}}
	through, _, err := steerPool(atPool, f.host(), false)
	if err == nil || !strings.Contains(err.Error(), "REDIRECT --to-ports 11581") || through {
		t.Errorf("a refused redirect: through=%v err=%v, want it named and the pool held", through, err)
	}
	f = &fakePoolHost{rules: bareRulesShown, natUnread: true}
	if _, _, err := steerPool(atPool, f.host(), false); err == nil {
		t.Error("iptables that cannot be read read as a steered pool")
	}
	if !slices.Contains(f.writes, "rule add from 10.248.48.0/24 lookup 1000006 priority 10013") {
		t.Errorf("writes %q: the pool was left with no rule, on main", f.writes)
	}
}

// iptables prints a network without host bits; a pool typed with them was never found
// in the listing, and its redirect would be added again on every pass.
func TestATypedNetworkWithHostBitsIsFound(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581})
	typed := PoolExit{CIDR: "10.90.0.1/24", Exit: "psi-AT", Table: 1_000_006}
	rules := "10012:\tfrom 10.90.0.1/24 to 10.90.0.1/24 iif lo lookup main\n" +
		"10012:\tfrom 10.90.0.1/24 ipproto udp dport 53 lookup main\n10013:\tfrom 10.90.0.1/24 lookup 1000006\n"
	f := &fakePoolHost{table: poolHeldShown, rules: rules, nat: redirectShown("10.90.0.0/24", "11581"), dnsNAT: true}
	if through, _, err := steerPool(typed, f.host(), true); err != nil || len(f.writes) != 0 || !through {
		t.Errorf("writes %q through=%v err=%v, want the redirect found", f.writes, through, err)
	}
}

// A pool taken away loses every rule from it, its DNS rule, redirect and DNS rewrite, and
// a table nothing looks up any more; another pool's keep what they had.
func TestReleasePoolTakesEverythingOfItsPoolAlone(t *testing.T) {
	f := &fakePoolHost{
		rules: "0:\tfrom all lookup local\n" +
			"7003:\tfrom 10.248.48.0/24 lookup 7003\n" +
			"10012:\tfrom 10.248.48.0/24 ipproto udp dport 53 lookup main\n" +
			"10013:\tfrom 10.248.48.0/24 lookup 1000006\n" +
			"10014:\tfrom 10.248.49.0/24 ipproto udp dport 53 lookup main\n" +
			"10015:\tfrom 10.248.49.0/24 lookup 1000008\n" +
			"32766:\tfrom all lookup main\n",
		nat:    redirectShown("10.248.48.0/24", "11581") + redirectShown("10.248.49.0/24", "11581"),
		dnsNAT: true,
	}
	if err := releasePool("10.248.48.0/24", f.host()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rule del priority 10012 from 10.248.48.0/24 ipproto udp dport 53 lookup main",
		"rule del priority 7003 from 10.248.48.0/24 lookup 7003",
		"rule del priority 10013 from 10.248.48.0/24 lookup 1000006",
		"route flush table 7003",
		"route flush table 1000006",
		dnsNATDel,
		redirectWrite("-D", "11581"),
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q", f.writes, want)
	}
}

// A release, run on every start of a pool with no exit, took an operator's rule into
// wg-quick's table and flushed that table, so WARP's traffic left from this server's address.
func TestAReleaseLeavesAnOperatorsRuleAndTableAlone(t *testing.T) {
	const operators = "32764:\tnot from all fwmark 0xca6c lookup 51820\n" +
		"32765:\tfrom 10.1.5.0/24 lookup 51820\n"
	f := &fakePoolHost{rules: "0:\tfrom all lookup local\n7605:\tfrom 10.1.5.0/24 lookup 7605\n" + operators + "32766:\tfrom all lookup main\n"}
	if err := releasePool("10.1.5.0/24", f.host()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rule del priority 7605 from 10.1.5.0/24 lookup 7605", "route flush table 7605"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q: only the old numbering's rule and table are this code's", f.writes, want)
	}

	// A start still takes every other rule from its own pool, which would leave by it, but
	// flushes no table it never wrote: wg-quick's here, which its `not` rule still uses.
	pool := PoolExit{CIDR: "10.1.5.0/24", Exit: "wgr9", Table: OpenVPNPoolTable(5)}
	f = &fakePoolHost{table: wgr9Shown, rules: "10022:\tfrom 10.1.5.0/24 to 10.1.5.0/24 iif lo lookup main\n10023:\tfrom 10.1.5.0/24 lookup 1000011\n" + operators, up: true}
	if _, _, err := steerPool(pool, f.host(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rule del priority 32765 from 10.1.5.0/24 lookup 51820"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q and table 51820 kept", f.writes, want)
	}
}

// fakeGuards is iptables and ip6tables with an INPUT listing each; it records every write.
type fakeGuards struct {
	listing map[string]string
	unread  map[string]bool
	writes  []string
	reads   int
}

func (f *fakeGuards) host() guardHost {
	run := func(family string) func(args ...string) (string, error) {
		return func(args ...string) (string, error) {
			if slices.Contains(args, "-S") {
				f.reads++
				if f.unread[family] {
					return family + ": command not found", errors.New("exit status 127")
				}
				return f.listing[family], nil
			}
			f.writes = append(f.writes, family+" "+strings.Join(args, " "))
			return "", nil
		}
	}
	return guardHost{v4: run("iptables"), v6: run("ip6tables")}
}

func guardShown(port, target string) string {
	state := "! --ctstate DNAT"
	if target == "ACCEPT" {
		state = "--ctstate DNAT"
	}
	return "-A INPUT -p tcp -m tcp --dport " + port + " -m conntrack " + state + ` -m comment --comment "3x-ui:psiphon" -j ` + target + "\n"
}

func guardWrite(family, action, port, target string) string {
	state := "! --ctstate DNAT"
	if target == "ACCEPT" {
		state = "--ctstate DNAT"
	}
	tcp := ""
	if action == "-D" {
		tcp = " -m tcp"
	}
	return family + " -w 5 " + action + " INPUT -p tcp" + tcp + " --dport " + port + " -m conntrack " + state + " -m comment --comment 3x-ui:psiphon -j " + target
}

// Xray takes redirected pools on 0.0.0.0, IPv6 too: without a guard the port would carry
// anyone's connection into Psiphon. Only what a pool's redirect brought gets through.
func TestTheRedirectPortsAreGuardedOnBothFamilies(t *testing.T) {
	const operators = "-A INPUT -p tcp -m tcp --dport 11590 -j DROP\n-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT\n"
	f := &fakeGuards{listing: map[string]string{
		"iptables": "-P INPUT ACCEPT\n" + guardShown("11581", "DROP") + guardShown("11590", "DROP") + operators,
	}}
	if _, err := fitGuards(f.host(), []int{11581, 11582}, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		guardWrite("iptables", "-D", "11590", "DROP"),
		guardWrite("iptables", "-I", "11581", "ACCEPT"),
		guardWrite("iptables", "-I", "11582", "DROP"),
		guardWrite("iptables", "-I", "11582", "ACCEPT"),
		guardWrite("ip6tables", "-I", "11581", "DROP"),
		guardWrite("ip6tables", "-I", "11582", "DROP"),
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("writes %q\nwant %q", f.writes, want)
	}

	// Before a start nothing is pruned, a guard doubled by two writers excepted.
	f = &fakeGuards{listing: map[string]string{
		"iptables":  guardShown("11581", "DROP") + guardShown("11581", "DROP") + guardShown("11581", "ACCEPT") + guardShown("11590", "DROP"),
		"ip6tables": guardShown("11581", "DROP"),
	}}
	if _, err := fitGuards(f.host(), []int{11581}, false); err != nil {
		t.Fatal(err)
	}
	if want := []string{guardWrite("iptables", "-D", "11581", "DROP")}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %q, want %q", f.writes, want)
	}
}

// The ports the running config takes are where pools are sent and what is guarded; a
// panel restart takes a guard an earlier run left, and a box with no Psiphon pays nothing.
func TestTheRunningConfigsPortsAreTheRegistry(t *testing.T) {
	redirectsAt(t, nil)
	psiphonRedirects.fitted = false

	f := &fakeGuards{
		listing: map[string]string{"iptables": guardShown("11590", "DROP")},
		unread:  map[string]bool{"ip6tables": true},
	}
	if _, err := setPsiphonRedirects(nil, f.host(), true); err != nil {
		t.Fatalf("a box without ip6tables and no port to guard: %v", err)
	}
	if want := []string{guardWrite("iptables", "-D", "11590", "DROP")}; !slices.Equal(f.writes, want) {
		t.Errorf("the first fit: writes %q, want %q", f.writes, want)
	}

	f = &fakeGuards{listing: map[string]string{}}
	if _, err := setPsiphonRedirects(nil, f.host(), true); err != nil || f.reads != 0 {
		t.Errorf("nothing before and nothing now: %d reads, err=%v, want none", f.reads, err)
	}
	if wrote, err := reassertPsiphonGuards(f.host()); wrote || err != nil || f.reads != 0 {
		t.Errorf("a pass with nothing to guard read %d times", f.reads)
	}

	running := map[string]int{"psi-AT": 11581}
	if changed, err := setPsiphonRedirects(running, f.host(), true); err != nil || !changed {
		t.Fatalf("a first port: changed=%v err=%v", changed, err)
	}
	if changed, _ := setPsiphonRedirects(map[string]int{"psi-AT": 11581}, f.host(), true); changed {
		t.Error("the same ports again read as a change, which sends every pool round again")
	}
	running["psi-AT"] = 1
	if got := psiphonRedirectPort(" psi-AT "); got != 11581 {
		t.Errorf("psi-AT is sent to %d, want 11581", got)
	}
	if got := psiphonRedirectPort("psi-FR"); got != 0 {
		t.Errorf("psi-FR, not in the config, is sent to %d", got)
	}

	// A flush takes the guards; the pass puts them back and says so.
	f.writes = nil
	wrote, err := reassertPsiphonGuards(f.host())
	if err != nil || !wrote || len(f.writes) != 3 {
		t.Errorf("after a flush: wrote=%v err=%v writes %q, want the three guards back", wrote, err, f.writes)
	}
	if !maps.Equal(psiphonRedirects.ports, map[string]int{"psi-AT": 11581}) {
		t.Errorf("registry %v", psiphonRedirects.ports)
	}
}

// A pass between a start's guard and its run pruned the new ports, which the core then
// listened on open; nor may that guard take the running config's, as a failed start keeps it.
func TestAStartingConfigsGuardsOutliveThePass(t *testing.T) {
	redirectsAt(t, map[string]int{"psi-AT": 11581, "psi-FR": 11582})
	both := guardShown("11581", "DROP") + guardShown("11581", "ACCEPT")
	f := &fakeGuards{listing: map[string]string{
		"iptables":  both + guardShown("11582", "DROP") + guardShown("11582", "ACCEPT"),
		"ip6tables": guardShown("11581", "DROP") + guardShown("11582", "DROP"),
	}}
	if err := guardPsiphonRedirects(map[string]int{"psi-AT": 11581, "psi-US": 11583}, f.host()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		guardWrite("iptables", "-I", "11583", "DROP"),
		guardWrite("iptables", "-I", "11583", "ACCEPT"),
		guardWrite("ip6tables", "-I", "11583", "DROP"),
	}
	if !slices.Equal(f.writes, want) {
		t.Errorf("before the start: writes %q\nwant %q, FR's guards kept", f.writes, want)
	}

	f.writes = nil
	f.listing["iptables"] += guardShown("11583", "DROP") + guardShown("11583", "ACCEPT")
	f.listing["ip6tables"] += guardShown("11583", "DROP")
	if _, err := reassertPsiphonGuards(f.host()); err != nil || len(f.writes) != 0 {
		t.Errorf("a pass before the start ran: writes %q err=%v, want every guard kept", f.writes, err)
	}

	if _, err := setPsiphonRedirects(map[string]int{"psi-AT": 11581, "psi-US": 11583}, f.host(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.writes, guardWrite("iptables", "-D", "11582", "DROP")) {
		t.Errorf("once the start ran: writes %q, want FR's guards gone", f.writes)
	}

	// A start that failed leaves its ports guarded only until the next one runs.
	if err := guardPsiphonRedirects(map[string]int{"psi-DE": 11584}, f.host()); err != nil {
		t.Fatal(err)
	}
	if _, err := setPsiphonRedirects(map[string]int{"psi-AT": 11581}, f.host(), true); err != nil {
		t.Fatal(err)
	}
	f.writes = nil
	f.listing["iptables"] += guardShown("11584", "DROP")
	if _, err := reassertPsiphonGuards(f.host()); err != nil || !slices.Contains(f.writes, guardWrite("iptables", "-D", "11584", "DROP")) {
		t.Errorf("a pass after the next start: writes %q err=%v, want DE's guard gone", f.writes, err)
	}
}
