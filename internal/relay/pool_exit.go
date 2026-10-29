package relay

import (
	"fmt"
	"net"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// A pool leaving through an exit has its own table and rule, a blackhole behind the exit's
// route: an empty table would hand the pool on to main, out of this server's own address.

// PsiphonExitPrefix starts an exit a Psiphon client carries: a cisco or OpenVPN pool on
// one has its TCP redirected into Xray, not routed to a relay's WireGuard tunnel.
const PsiphonExitPrefix = "psi-"

// IsPsiphonExit reports whether an exit is a Psiphon country rather than a relay.
func IsPsiphonExit(iface string) bool {
	return strings.HasPrefix(strings.TrimSpace(iface), PsiphonExitPrefix)
}

// poolHoldMetric puts the blackhole behind the exit's own route, which has none.
const poolHoldMetric = "4000"

// Pool tables start far above main's 253-255 and the Xray exits' 60000 block: a cisco
// location's are even and an OpenVPN inbound's odd, so no two ids ever share one.
const poolTableBase = 1_000_000

// CiscoPoolTable is the table of the cisco location in slot: its inbound's id times
// the locations an inbound may have, plus its index.
func CiscoPoolTable(slot int) int { return poolTableBase + 2*absInt(slot) }

// OpenVPNPoolTable is the table of an OpenVPN inbound's pool.
func OpenVPNPoolTable(inboundID int) int { return poolTableBase + 2*absInt(inboundID) + 1 }

// A pool's rule takes an odd priority from 10001 to 29999, its DNS rule the one before:
// ahead of main's 32766 whatever the table, and pools sharing one differ by source.
const (
	poolPriorityBase  = 10_001
	poolPrioritySlots = 10_000
)

func poolPriority(table int) int {
	return poolPriorityBase + 2*(absInt(table-poolTableBase)%poolPrioritySlots)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// PoolExit is one pool sent out of an exit through its own table and rule.
type PoolExit struct {
	CIDR  string
	Exit  string
	Table int
}

// poolTable is what `ip route show table T` lists of a pool's default routes.
type poolTable struct {
	held bool
	devs []string
}

func readPoolTable(out string) poolTable {
	var t poolTable
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 4 && f[0] == "blackhole" && f[1] == "default":
			if i := slices.Index(f, "metric"); i > 0 && i+1 < len(f) && f[i+1] == poolHoldMetric {
				t.held = true
			}
		case len(f) >= 3 && f[0] == "default" && f[1] == "dev":
			t.devs = append(t.devs, f[2])
		}
	}
	return t
}

// ipRule is one line of `ip rule show`: its priority, its source, any further selector,
// and the table it looks up, "" for another action.
type ipRule struct {
	prio, from string
	match      []string
	lookup     string
}

// readRules skips a `not from` rule: no pool's own, and the table it looks up, wg-quick's
// for one, is never this code's, which is the only kind a sweep flushes.
func readRules(out string) []ipRule {
	var rules []ipRule
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasSuffix(f[0], ":") || f[1] != "from" {
			continue
		}
		r := ipRule{prio: strings.TrimSuffix(f[0], ":"), from: f[2], match: f[3:]}
		if i := slices.Index(r.match, "lookup"); i >= 0 && i+1 < len(r.match) {
			r.match, r.lookup = r.match[:i], r.match[i+1]
		}
		rules = append(rules, r)
	}
	return rules
}

// exactlyFrom reports whether the rule selects cidr and nothing more, into a table.
func (r ipRule) exactlyFrom(cidr string) bool {
	return r.from == cidr && len(r.match) == 0 && r.lookup != ""
}

// legacyPoolTableBase is where the old numbering began, cisco's 7000 + id*16 + index and
// OpenVPN's 7600 + id%300, each rule at its own table's number as priority.
const legacyPoolTableBase = 7000

// panelRule reports whether this code wrote the rule, under the old numbering or this one;
// an operator's rule into a table of their own is never released, nor its table flushed.
func (r ipRule) panelRule() bool {
	n, err := strconv.Atoi(r.lookup)
	switch {
	case err != nil:
		return false
	case n >= poolTableBase:
		return r.prio == strconv.Itoa(poolPriority(n))
	}
	return n >= legacyPoolTableBase && r.prio == r.lookup
}

// steers reports whether the rule sends exactly cidr into table: by its number, or at the
// pool's priority when rt_tables names the table and the listing prints the name.
func (r ipRule) steers(cidr string, table int) bool {
	if !r.exactlyFrom(cidr) {
		return false
	}
	if r.lookup == strconv.Itoa(table) {
		return true
	}
	_, err := strconv.Atoi(r.lookup)
	return err != nil && r.prio == strconv.Itoa(poolPriority(table))
}

// dnsSelector picks a Psiphon pool's DNS, which leaves from this server as an Xray host's does.
var dnsSelector = []string{"ipproto", "udp", "dport", "53"}

// localSelector picks what this server sends its own pool, a redirected connection's answer
// above all: it leaves from the device's address, inside the pool, so the pool rule took it.
func localSelector(cidr string) []string { return []string{"to", cidr, "iif", "lo"} }

// sendsToMain reports whether the rule sends what sel picks of cidr to main.
func (r ipRule) sendsToMain(cidr string, sel []string) bool {
	if r.from != cidr || (r.lookup != "main" && r.lookup != "254") || len(r.match) != len(sel) {
		return false
	}
	for i, m := range r.match {
		// ip prints a protocol /etc/protocols does not name by its number.
		if m != sel[i] && !(i > 0 && sel[i-1] == "ipproto" && sel[i] == "udp" && m == "17") {
			return false
		}
	}
	return true
}

// mainRuleSteps keeps cidr's one rule sending sel to main at prio, or none at prio 0. A
// reassert only adds one where there is none: two pools given one network would trade it.
func mainRuleSteps(rules []ipRule, cidr string, sel []string, prio int, reassert bool) []poolStep {
	is := func(r ipRule) bool { return r.sendsToMain(cidr, sel) }
	if reassert && slices.ContainsFunc(rules, is) {
		return nil
	}
	want := strconv.Itoa(prio)
	have := prio > 0 && slices.ContainsFunc(rules, func(r ipRule) bool { return is(r) && r.prio == want })
	var steps []poolStep
	if prio > 0 && !have {
		steps = append(steps, poolStep{args: slices.Concat([]string{"rule", "add", "from", cidr}, sel, []string{"lookup", "main", "priority", want})})
	}
	kept := false
	for _, r := range rules {
		if !is(r) {
			continue
		}
		if have && r.prio == want && !kept {
			kept = true
			continue
		}
		steps = append(steps, poolStep{args: slices.Concat([]string{"rule", "del", "priority", r.prio, "from", cidr}, sel, []string{"lookup", "main"}), mayFail: true})
	}
	return steps
}

// PoolRuleIn reports whether `ip rule show` output holds the rule sending cidr into table.
func PoolRuleIn(out, cidr string, table int) bool {
	return slices.ContainsFunc(readRules(out), func(r ipRule) bool { return r.steers(cidr, table) })
}

// poolStep is one ip or iptables command. One that may fail writes on a device that can
// vanish between the read and the write, or tidies; either leaves the pool held.
type poolStep struct {
	iptables bool
	args     []string
	mayFail  bool
	toExit   bool
}

// poolSteps brings a pool's table and rules to what p needs, the blackhole first so the
// rule never points at an empty table. A reassert leaves what two writers could share.
func poolSteps(p PoolExit, have poolTable, rules []ipRule, deviceUp, reassert bool) []poolStep {
	table := strconv.Itoa(p.Table)
	psi := IsPsiphonExit(p.Exit)
	var steps []poolStep
	if !have.held {
		steps = append(steps, poolStep{args: []string{"route", "replace", "blackhole", "default", "table", table, "metric", poolHoldMetric}})
	}
	other := slices.ContainsFunc(have.devs, func(dev string) bool { return dev != p.Exit })
	// A route left to a previous exit would carry the pool to the wrong country while
	// its new device is missing; a Psiphon pool's table holds nothing but the blackhole.
	for _, dev := range have.devs {
		if psi || (dev != p.Exit && !reassert) {
			steps = append(steps, poolStep{args: []string{"route", "del", "default", "dev", dev, "table", table}, mayFail: true})
		}
	}
	if !psi && deviceUp && !slices.Contains(have.devs, p.Exit) && !(reassert && other) {
		steps = append(steps, poolStep{args: []string{"route", "replace", "default", "dev", p.Exit, "table", table}, mayFail: true, toExit: true})
	}
	if !slices.ContainsFunc(rules, func(r ipRule) bool { return r.steers(p.CIDR, p.Table) }) {
		steps = append(steps, poolStep{args: []string{"rule", "add", "from", p.CIDR, "lookup", table, "priority", strconv.Itoa(poolPriority(p.Table))}})
	}
	ahead := poolPriority(p.Table) - 1
	steps = append(steps, mainRuleSteps(rules, p.CIDR, localSelector(p.CIDR), ahead, reassert)...)
	switch {
	case psi:
		steps = append(steps, mainRuleSteps(rules, p.CIDR, dnsSelector, ahead, reassert)...)
	case !reassert:
		steps = append(steps, mainRuleSteps(rules, p.CIDR, dnsSelector, 0, false)...)
	}
	if !reassert {
		steps = append(steps, strayRuleSteps(rules, p.CIDR, func(r ipRule) bool { return !r.steers(p.CIDR, p.Table) })...)
	}
	return steps
}

// strayRuleSteps takes out the rules sending exactly cidr into a table stray picks, then
// flushes a table of this code's own that no rule left looks up.
func strayRuleSteps(rules []ipRule, cidr string, stray func(ipRule) bool) []poolStep {
	var steps []poolStep
	gone := make([]bool, len(rules))
	for i, r := range rules {
		if !r.exactlyFrom(cidr) || !stray(r) {
			continue
		}
		gone[i] = true
		steps = append(steps, poolStep{args: []string{"rule", "del", "priority", r.prio, "from", cidr, "lookup", r.lookup}, mayFail: true})
	}
	var flushed []string
	for i, r := range rules {
		if !gone[i] || !r.panelRule() || slices.Contains(flushed, r.lookup) {
			continue
		}
		lookedUp := false
		for j, other := range rules {
			lookedUp = lookedUp || (!gone[j] && other.lookup == r.lookup)
		}
		if !lookedUp {
			flushed = append(flushed, r.lookup)
			steps = append(steps, poolStep{args: []string{"route", "flush", "table", r.lookup}, mayFail: true})
		}
	}
	return steps
}

// poolHost is the part of the system a pool's steering reads and writes.
type poolHost struct {
	ip       func(args ...string) (string, error)
	iptables func(args ...string) (string, error)
	deviceUp func(name string) bool
}

var localPoolHost = poolHost{
	ip:       func(args ...string) (string, error) { return runLocalOut("ip", args...) },
	iptables: func(args ...string) (string, error) { return runLocalOut("iptables", args...) },
	deviceUp: func(name string) bool {
		ifc, err := net.InterfaceByName(name)
		return err == nil && ifc.Flags&net.FlagUp != 0
	},
}

// SteerPool writes whatever p's table and rules are missing, so a reconcile can repeat
// it, and takes out what a previous exit or numbering left. through is false while held.
func SteerPool(p PoolExit) (through, wrote bool, err error) {
	if runtime.GOOS != "linux" {
		return false, false, nil
	}
	return steerPool(p, localPoolHost, false)
}

// ReassertPool is SteerPool for the periodic pass: it writes only what is missing and
// leaves a route to another exit alone.
func ReassertPool(p PoolExit) (through, wrote bool, err error) {
	if runtime.GOOS != "linux" {
		return false, false, nil
	}
	return steerPool(p, localPoolHost, true)
}

// missingTable is how `ip route show` answers for a table nothing has written since
// boot (exit status 2); the blackhole's write is what creates it.
const missingTable = "FIB table does not exist"

// steerPool brings p to its exit. A Psiphon pool's TCP goes to the port Xray now takes its
// country on; with none the pool stays held at its blackhole.
func steerPool(p PoolExit, h poolHost, reassert bool) (through, wrote bool, err error) {
	table := strconv.Itoa(p.Table)
	routes, err := h.ip("route", "show", "table", table)
	if err != nil && !strings.Contains(routes, missingTable) {
		return false, false, fmt.Errorf("read table %s: %w: %s", table, err, firstLines(routes, 1))
	}
	if err != nil {
		routes = ""
	}
	rules, err := h.ip("rule", "show")
	if err != nil {
		return false, false, fmt.Errorf("read the routing rules: %w: %s", err, firstLines(rules, 1))
	}
	have := readPoolTable(routes)
	psi := IsPsiphonExit(p.Exit)
	through = !psi && slices.Contains(have.devs, p.Exit)
	reached, wrote, err := runPoolSteps(h, poolSteps(p, have, readRules(rules), h.deviceUp(p.Exit), reassert))
	through = through || reached
	if err != nil {
		return false, wrote, err
	}
	// A start takes what a Psiphon exit left on a pool now on a relay, and the pass retries
	// only what a start or release could not; a relay's route stands either way.
	if psi || !reassert || natUnswept(p.CIDR) {
		redirected, wroteNAT, natErr := steerPsiphonNAT(p.CIDR, h, psi, psiphonRedirectPort(p.Exit))
		wrote = wrote || wroteNAT
		switch {
		case psi && natErr != nil:
			return false, wrote, natErr
		case psi:
			through = redirected
		case natErr != nil && reassert:
			return through, wrote, natErr
		}
	}
	if !through && reassert && !psi && len(have.devs) > 0 {
		return false, wrote, fmt.Errorf("table %s leaves through %s, not %s: something else writes this pool's table", table, strings.Join(have.devs, ", "), p.Exit)
	}
	return through, wrote, nil
}

// runPoolSteps runs steps in order: reached is a toExit step that took, and a refused
// step that may not fail stops the rest.
func runPoolSteps(h poolHost, steps []poolStep) (reached, wrote bool, err error) {
	for _, s := range steps {
		run, name := h.ip, "ip"
		if s.iptables {
			run, name = h.iptables, "iptables"
		}
		out, runErr := run(s.args...)
		if runErr == nil {
			wrote = true
			reached = reached || s.toExit
			continue
		}
		if !s.mayFail {
			return reached, wrote, fmt.Errorf("%s %s: %w: %s", name, strings.Join(s.args, " "), runErr, firstLines(out, 1))
		}
	}
	return reached, wrote, nil
}
