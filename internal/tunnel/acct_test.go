package tunnel

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeNetfilter answers the iptables commands the counting runs the way iptables 1.8.10
// does: -S prints each rule back with its comment quoted, and -v adds its counters.
type fakeNetfilter struct {
	t        *testing.T
	chains   map[string][]string // rule bodies after "-A <chain> ", in order
	user     map[string]bool
	bytes    map[string]int64 // by chain + " " + body
	commands [][]string
	absent   bool // the binary is not installed
}

var errNoChain = errors.New("exit status 1: iptables: No chain/target/match by that name.")

func newFakeNetfilter(t *testing.T) *fakeNetfilter {
	return &fakeNetfilter{
		t: t, user: map[string]bool{}, bytes: map[string]int64{},
		chains: map[string][]string{
			"INPUT": {
				`-p tcp -m tcp --dport 11583 -m conntrack ! --ctstate DNAT -m comment --comment "3x-ui:psiphon" -j DROP`,
				`-s 178.162.203.0/24 -j DROP`,
			},
			"OUTPUT": {`-d 178.162.203.0/24 -j DROP`},
		},
	}
}

// render is a rule as -S prints it: a comment with anything but letters, digits, _ or - is quoted.
func render(spec []string) string {
	out := slices.Clone(spec)
	for i := 1; i < len(out); i++ {
		if out[i-1] == "--comment" {
			out[i] = strconv.Quote(out[i])
		}
	}
	return strings.Join(out, " ")
}

func (f *fakeNetfilter) run(args ...string) (string, error) {
	f.t.Helper()
	if f.absent {
		return "", &exec.Error{Name: "ip6tables", Err: exec.ErrNotFound}
	}
	if len(args) < 4 || args[0] != "-w" || args[1] != "5" {
		f.t.Fatalf("iptables %v does not wait for the lock", args)
	}
	f.commands = append(f.commands, args)
	op, chain, rest := args[2], args[3], args[4:]
	rules, exists := f.chains[chain]
	if !exists && op != "-N" {
		return "", errNoChain
	}
	switch op {
	case "-S":
		head := "-P " + chain + " ACCEPT\n"
		if f.user[chain] {
			head = "-N " + chain + "\n"
		}
		for _, r := range rules {
			if slices.Contains(rest, "-v") {
				r = strings.Replace(r, " -j ", fmt.Sprintf(" -c 1 %d -j ", f.bytes[chain+" "+r]), 1)
			}
			head += "-A " + chain + " " + r + "\n"
		}
		return head, nil
	case "-N":
		if exists {
			return "", errors.New("exit status 1: iptables: Chain already exists.")
		}
		f.chains[chain], f.user[chain] = []string{}, true
	case "-A":
		f.chains[chain] = append(rules, render(rest))
	case "-I":
		pos := 0
		if n, err := strconv.Atoi(rest[0]); err == nil {
			pos, rest = n-1, rest[1:]
		}
		f.chains[chain] = slices.Insert(rules, pos, render(rest))
	case "-D":
		i := slices.Index(rules, render(rest))
		if i < 0 {
			return "", errors.New("exit status 1: iptables: Bad rule (does a matching rule exist in that chain?).")
		}
		delete(f.bytes, chain+" "+rules[i])
		f.chains[chain] = slices.Delete(rules, i, i+1)
	case "-F":
		f.chains[chain] = []string{}
	case "-X":
		for _, rs := range f.chains {
			for _, r := range rs {
				if strings.HasSuffix(r, "-j "+chain) {
					return "", errors.New("exit status 1: iptables: Too many links.")
				}
			}
		}
		if len(rules) > 0 {
			return "", errors.New("exit status 1: iptables: Directory not empty.")
		}
		delete(f.chains, chain)
		delete(f.user, chain)
	default:
		f.t.Fatalf("the fake does not know iptables %v", args)
	}
	return "", nil
}

// writes is every command since the last call that changed something.
func (f *fakeNetfilter) writes() [][]string {
	var out [][]string
	for _, c := range f.commands {
		if c[2] != "-S" {
			out = append(out, c)
		}
	}
	f.commands = nil
	return out
}

func (f *fakeNetfilter) count(reading string, n int64) {
	for _, r := range f.chains[acctChain] {
		if strings.Contains(r, `"`+acctMark+":"+reading+`"`) {
			f.bytes[acctChain+" "+r] = n
		}
	}
}

func wantChain(t *testing.T, f *fakeNetfilter, chain string, want []string) {
	t.Helper()
	if got := f.chains[chain]; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s holds\n  %s\nwant\n  %s", chain, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

const acctJump = `-m comment --comment "3x-ui:front-acct" -j X_UI_FRONT_ACCT`

var (
	wsFront    = Pair{Id: 7, FrontAddress: "203.0.113.9", TunnelPort: 3080, Transport: TransportWS, Tool: ToolBackhaul}
	udpFront   = Pair{Id: 11, FrontAddress: " 198.51.100.11 ", TunnelPort: 5000, Transport: TransportUDP}
	v6Front    = Pair{Id: 10, FrontAddress: "2001:db8::a", TunnelPort: 4000, Transport: TransportUDPoTCP, Tool: ToolBackhaul}
	bpFront    = Pair{Id: 8, FrontAddress: "203.0.113.8", TunnelPort: 3080, Transport: TransportStealth, Tool: ToolBackPack}
	namedFront = Pair{Id: 9, FrontAddress: "ir1.example.ir", TunnelPort: 3080, Transport: TransportTCP}
)

// What the panel sends a front is the customers' download and what comes back their
// upload, on each Backhaul leg's own port and protocol; a BackPack front counts itself.
func TestEachBackhaulLegIsCountedBothWaysAheadOfEveryOtherRule(t *testing.T) {
	v4, v6 := newFakeNetfilter(t), newFakeNetfilter(t)
	tables := &acctTables{v4: v4.run, v6: v6.run}
	if err := tables.fit([]Pair{wsFront, bpFront, namedFront, v6Front, udpFront}); err != nil {
		t.Fatal(err)
	}
	wantChain(t, v4, acctChain, []string{
		`-d 203.0.113.9/32 -p tcp -m tcp --dport 3080 -m comment --comment "3x-ui:front-acct:7:out" -j RETURN`,
		`-s 203.0.113.9/32 -p tcp -m tcp --sport 3080 -m comment --comment "3x-ui:front-acct:7:in" -j RETURN`,
		`-d 203.0.113.9/32 -p udp -m udp --dport 3081 -m comment --comment "3x-ui:front-acct:7-udp:out" -j RETURN`,
		`-s 203.0.113.9/32 -p udp -m udp --sport 3081 -m comment --comment "3x-ui:front-acct:7-udp:in" -j RETURN`,
		`-d 198.51.100.11/32 -p udp -m udp --dport 5000 -m comment --comment "3x-ui:front-acct:11:out" -j RETURN`,
		`-s 198.51.100.11/32 -p udp -m udp --sport 5000 -m comment --comment "3x-ui:front-acct:11:in" -j RETURN`,
	})
	wantChain(t, v6, acctChain, []string{
		`-d 2001:db8::a/128 -p tcp -m tcp --dport 4000 -m comment --comment "3x-ui:front-acct:10:out" -j RETURN`,
		`-s 2001:db8::a/128 -p tcp -m tcp --sport 4000 -m comment --comment "3x-ui:front-acct:10:in" -j RETURN`,
	})
	for _, f := range []*fakeNetfilter{v4, v6} {
		wantChain(t, f, "INPUT", []string{
			acctJump,
			`-p tcp -m tcp --dport 11583 -m conntrack ! --ctstate DNAT -m comment --comment "3x-ui:psiphon" -j DROP`,
			`-s 178.162.203.0/24 -j DROP`,
		})
		wantChain(t, f, "OUTPUT", []string{acctJump, `-d 178.162.203.0/24 -j DROP`})
	}
}

// The minute's refit of a front that has not changed writes nothing, so its
// counters run on; a front gone or moved changes its own rules and no other's.
func TestARefitTouchesOnlyTheFrontsThatChanged(t *testing.T) {
	v4, v6 := newFakeNetfilter(t), newFakeNetfilter(t)
	tables := &acctTables{v4: v4.run, v6: v6.run}
	if err := tables.fit([]Pair{wsFront, udpFront}); err != nil {
		t.Fatal(err)
	}
	v4.writes()
	v4.count("11:out", 5000)
	if err := tables.fit([]Pair{wsFront, udpFront}); err != nil {
		t.Fatal(err)
	}
	if w := v4.writes(); len(w) != 0 {
		t.Fatalf("an unchanged refit wrote %v", w)
	}

	moved := wsFront
	moved.FrontAddress = "203.0.113.44"
	if err := tables.fit([]Pair{moved}); err != nil {
		t.Fatal(err)
	}
	wantChain(t, v4, acctChain, []string{
		`-d 203.0.113.44/32 -p tcp -m tcp --dport 3080 -m comment --comment "3x-ui:front-acct:7:out" -j RETURN`,
		`-s 203.0.113.44/32 -p tcp -m tcp --sport 3080 -m comment --comment "3x-ui:front-acct:7:in" -j RETURN`,
		`-d 203.0.113.44/32 -p udp -m udp --dport 3081 -m comment --comment "3x-ui:front-acct:7-udp:out" -j RETURN`,
		`-s 203.0.113.44/32 -p udp -m udp --sport 3081 -m comment --comment "3x-ui:front-acct:7-udp:in" -j RETURN`,
	})
	v4.writes()
	v4.count("7:out", 70)
	if err := tables.fit([]Pair{moved, udpFront}); err != nil {
		t.Fatal(err)
	}
	read, err := tables.read([]Pair{moved, udpFront}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := read[7]["7:out"].BytesOut; got != 70 {
		t.Errorf("adding a front back reset the one that stayed: 7:out reads %d, want 70", got)
	}
	for _, c := range v4.writes() {
		if strings.Contains(strings.Join(c, " "), "front-acct:7") {
			t.Errorf("adding front 11 back rewrote front 7: %v", c)
		}
	}
}

// With no Backhaul front left the jumps and the chain go and every other rule
// stays; after that, and on a panel that never had one, no iptables runs at all.
func TestNoBackhaulFrontLeavesNothingAndThenRunsNothing(t *testing.T) {
	v4, v6 := newFakeNetfilter(t), newFakeNetfilter(t)
	tables := &acctTables{v4: v4.run, v6: v6.run}
	if err := tables.fit([]Pair{wsFront, v6Front}); err != nil {
		t.Fatal(err)
	}
	if err := tables.fit([]Pair{bpFront}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*fakeNetfilter{v4, v6} {
		if _, ok := f.chains[acctChain]; ok {
			t.Errorf("the chain outlived the last Backhaul front: %v", f.chains[acctChain])
		}
		wantChain(t, f, "INPUT", []string{
			`-p tcp -m tcp --dport 11583 -m conntrack ! --ctstate DNAT -m comment --comment "3x-ui:psiphon" -j DROP`,
			`-s 178.162.203.0/24 -j DROP`,
		})
		wantChain(t, f, "OUTPUT", []string{`-d 178.162.203.0/24 -j DROP`})
		f.writes()
	}
	if err := tables.fit(nil); err != nil {
		t.Fatal(err)
	}
	if len(v4.commands)+len(v6.commands) != 0 {
		t.Errorf("a panel with nothing to count still ran %v %v", v4.commands, v6.commands)
	}

	fresh, missing := newFakeNetfilter(t), newFakeNetfilter(t)
	missing.absent = true
	tables = &acctTables{v4: fresh.run, v6: missing.run}
	for range 2 {
		if err := tables.fit(nil); err != nil {
			t.Fatalf("a box that never counted, one without ip6tables: %v", err)
		}
	}
	if len(fresh.commands) != 1 || fresh.commands[0][2] != "-S" {
		t.Errorf("a box that never counted ran %v; want one look for the chain", fresh.commands)
	}

	// A container without NET_ADMIN, as docker-compose allows with Fail2ban off.
	asked := 0
	tables = &acctTables{v4: func(...string) (string, error) {
		asked++
		return "", errors.New("exit status 4: iptables v1.8.10 (nf_tables): Could not fetch rule set generation id: Permission denied (you must be root)")
	}, v6: missing.run}
	for range 3 {
		if err := tables.fit(nil); err != nil {
			t.Fatalf("a firewall that refuses, with no front to count: %v", err)
		}
	}
	if asked != 1 {
		t.Errorf("a firewall that refuses was asked %d times; want once", asked)
	}
}

// A flushed INPUT, a restored table or a second jump would stop the counting or
// double it; the next fit puts back one jump each and every rule.
func TestAFlushedOrDoubledTableIsPutRight(t *testing.T) {
	v4, v6 := newFakeNetfilter(t), newFakeNetfilter(t)
	tables := &acctTables{v4: v4.run, v6: v6.run}
	if err := tables.fit([]Pair{udpFront}); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(v4.chains[acctChain])
	v4.chains["INPUT"] = []string{`-s 178.162.203.0/24 -j DROP`}
	v4.chains["OUTPUT"] = append(v4.chains["OUTPUT"], acctJump)
	v4.chains[acctChain] = want[:1]
	if err := tables.fit([]Pair{udpFront}); err != nil {
		t.Fatal(err)
	}
	wantChain(t, v4, "INPUT", []string{acctJump, `-s 178.162.203.0/24 -j DROP`})
	wantChain(t, v4, "OUTPUT", []string{acctJump, `-d 178.162.203.0/24 -j DROP`})
	wantChain(t, v4, acctChain, want)

	delete(v4.chains, acctChain)
	delete(v4.user, acctChain)
	v4.chains["INPUT"] = []string{}
	v4.chains["OUTPUT"] = []string{}
	if err := tables.fit([]Pair{udpFront}); err != nil {
		t.Fatal(err)
	}
	wantChain(t, v4, acctChain, want)
	wantChain(t, v4, "INPUT", []string{acctJump})
	wantChain(t, v4, "OUTPUT", []string{acctJump})
}

// Each counter is its front's reading by leg and direction, a rule listed twice adding both; one
// of its id at another address (before an Arvan swap's refit, the old box's mid-import) is not its.
func TestTheCountersAreReadByFrontLegAndDirection(t *testing.T) {
	listing := `-N X_UI_FRONT_ACCT
-A X_UI_FRONT_ACCT -d 203.0.113.9/32 -p tcp -m tcp --dport 3080 -m comment --comment "3x-ui:front-acct:7:out" -c 521040 699355553 -j RETURN
-A X_UI_FRONT_ACCT -s 203.0.113.9/32 -p tcp -m tcp --sport 3080 -m comment --comment "3x-ui:front-acct:7:in" -c 410200 75264133 -j RETURN
-A X_UI_FRONT_ACCT -d 203.0.113.9/32 -p udp -m udp --dport 3081 -m comment --comment "3x-ui:front-acct:7-udp:out" -c 12 4096 -j RETURN
-A X_UI_FRONT_ACCT -s 203.0.113.9/32 -p udp -m udp --sport 3081 -m comment --comment "3x-ui:front-acct:7-udp:in" -c 3 900 -j RETURN
-A X_UI_FRONT_ACCT -s 203.0.113.9/32 -p tcp -m tcp --sport 3080 -m comment --comment "3x-ui:front-acct:7:in" -c 0 0 -j RETURN
-A X_UI_FRONT_ACCT -d 198.51.100.11/32 -p udp -m udp --dport 5000 -m comment --comment "3x-ui:front-acct:11:out" -c 100 20000 -j RETURN
-A X_UI_FRONT_ACCT -d 198.51.100.200/32 -p udp -m udp --dport 5000 -m comment --comment "3x-ui:front-acct:11:in" -c 5 42949672960 -j RETURN
`
	now := time.Date(2026, 10, 29, 12, 0, 30, 0, time.UTC)
	tables := &acctTables{
		v4:    func(args ...string) (string, error) { return listing, nil },
		v6:    func(args ...string) (string, error) { return "", errNoChain },
		holds: [2]bool{true, false},
	}
	got, err := tables.read([]Pair{wsFront, udpFront, bpFront}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]map[string]LegMetrics{
		7: {
			"7:out":     {Taken: now, BytesOut: 699355553},
			"7:in":      {Taken: now, BytesIn: 75264133},
			"7-udp:out": {Taken: now, BytesOut: 4096},
			"7-udp:in":  {Taken: now, BytesIn: 900},
		},
		11: {"11:out": {Taken: now, BytesOut: 20000}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read %v\nwant %v", got, want)
	}
}

// Only a family the last fit left rules in is read: a box whose fronts are all on
// IPv4 must not report ip6tables' missing chain twice a minute.
func TestOnlyAFamilyHoldingRulesIsRead(t *testing.T) {
	v4 := newFakeNetfilter(t)
	v6Calls := 0
	tables := &acctTables{v4: v4.run, v6: func(args ...string) (string, error) {
		v6Calls++
		return "", errNoChain
	}}
	if err := tables.fit([]Pair{udpFront}); err != nil {
		t.Fatal(err)
	}
	v4.count("11:in", 900)
	before := v6Calls
	got, err := tables.read([]Pair{udpFront, v6Front}, time.Now())
	if err != nil {
		t.Fatalf("reading an IPv4-only panel failed: %v", err)
	}
	if v6Calls != before {
		t.Errorf("ip6tables was read with nothing of ours in it")
	}
	if got[11]["11:in"].BytesIn != 900 {
		t.Errorf("read %v; want 900 bytes in on 11:in", got)
	}
}
