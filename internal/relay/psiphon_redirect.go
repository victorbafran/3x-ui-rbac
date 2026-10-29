package relay

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// A pool on a Psiphon exit sends its TCP into Xray by a REDIRECT, so the customer's TCP
// ends in this kernel; its DNS leaves from here, and everything else meets the blackhole.

// psiphonMark is the comment on every netfilter rule written for a Psiphon redirect, so a
// sweep takes its own and never an operator's.
const psiphonMark = "3x-ui:psiphon"

// iptablesWait is iptables' -w: two jobs and an Xray start can write at once.
var iptablesWait = []string{"-w", "5"}

func iptablesArgs(args ...string) []string { return append(slices.Clone(iptablesWait), args...) }

// canonicalCIDR is cidr as iptables -S prints it, with no host bits.
func canonicalCIDR(cidr string) string {
	if _, n, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
		return n.String()
	}
	return strings.TrimSpace(cidr)
}

func redirectRule(cidr string, port int) []string {
	return []string{"-s", canonicalCIDR(cidr), "-p", "tcp", "-m", "comment", "--comment", psiphonMark, "-j", "REDIRECT", "--to-ports", strconv.Itoa(port)}
}

// dnsNATRule rewrites a pool's DNS to this server's address: the one part of it that
// leaves from here, as an Xray host's DNS does.
func dnsNATRule(cidr string) []string {
	return []string{"-s", canonicalCIDR(cidr), "-p", "udp", "--dport", "53", "-m", "comment", "--comment", psiphonMark, "-j", "MASQUERADE"}
}

// natLine is one rule of an `iptables -S` listing, quotes taken off, -A and chain first.
type natLine []string

func readNetfilter(listing, chain string) []natLine {
	var out []natLine
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "-A" || f[1] != chain {
			continue
		}
		for i := range f {
			f[i] = strings.Trim(f[i], `"`)
		}
		out = append(out, f)
	}
	return out
}

func (l natLine) value(flag string) string {
	if i := slices.Index(l, flag); i >= 0 && i+1 < len(l) {
		return l[i+1]
	}
	return ""
}

func (l natLine) marked() bool { return l.value("--comment") == psiphonMark }

// deleteArgs deletes this very rule; -S printed it as it is matched.
func (l natLine) deleteArgs(table string) []string {
	args := slices.Clone(iptablesWait)
	if table != "" {
		args = append(args, "-t", table)
	}
	return append(append(args, "-D"), l[1:]...)
}

// poolRedirects is cidr's redirects in `iptables -t nat -S PREROUTING`.
func poolRedirects(listing, cidr string) []natLine {
	var out []natLine
	for _, l := range readNetfilter(listing, "PREROUTING") {
		if l.marked() && l.value("-s") == canonicalCIDR(cidr) && l.value("-j") == "REDIRECT" {
			out = append(out, l)
		}
	}
	return out
}

// psiphonNATSteps sends cidr's TCP to port alone, its DNS out from here; port 0 or keep
// false (a pool with no Psiphon exit) takes every redirect away and the DNS rewrite too.
func psiphonNATSteps(cidr string, redirects []natLine, dnsNAT bool, port int, keep bool) (steps []poolStep, redirected bool) {
	want := strconv.Itoa(port)
	if keep && !dnsNAT {
		steps = append(steps, poolStep{iptables: true, args: iptablesArgs(append([]string{"-t", "nat", "-A", "POSTROUTING"}, dnsNATRule(cidr)...)...)})
	}
	if !keep && dnsNAT {
		steps = append(steps, poolStep{iptables: true, args: iptablesArgs(append([]string{"-t", "nat", "-D", "POSTROUTING"}, dnsNATRule(cidr)...)...)})
	}
	redirected = keep && port > 0 && slices.ContainsFunc(redirects, func(l natLine) bool { return l.value("--to-ports") == want })
	if keep && port > 0 && !redirected {
		steps = append(steps, poolStep{iptables: true, args: iptablesArgs(append([]string{"-t", "nat", "-A", "PREROUTING"}, redirectRule(cidr, port)...)...), toExit: true})
	}
	// The new redirect goes in before the old ones go, so a moved port leaves no gap.
	kept := false
	for _, l := range redirects {
		if keep && port > 0 && l.value("--to-ports") == want && !kept {
			kept = true
			continue
		}
		steps = append(steps, poolStep{iptables: true, args: l.deleteArgs("nat")})
	}
	return steps, redirected
}

// unswept is the pools a start or release could not take a Psiphon exit's redirect off,
// which may still hold one: the ten-second pass sweeps each until that works.
var unswept = struct {
	sync.Mutex
	cidrs map[string]bool
}{cidrs: map[string]bool{}}

func natUnswept(cidr string) bool {
	unswept.Lock()
	defer unswept.Unlock()
	return unswept.cidrs[canonicalCIDR(cidr)]
}

func markUnswept(cidr string, left bool) {
	unswept.Lock()
	defer unswept.Unlock()
	if left {
		unswept.cidrs[canonicalCIDR(cidr)] = true
	} else {
		delete(unswept.cidrs, canonicalCIDR(cidr))
	}
}

// steerPsiphonNAT reads cidr's netfilter state and brings it to psiphonNATSteps. Without
// iptables no redirect can be there; a pool with no Psiphon exit left as it was is unswept.
func steerPsiphonNAT(cidr string, h poolHost, psi bool, port int) (redirected, wrote bool, err error) {
	listing, err := h.iptables(iptablesArgs("-t", "nat", "-S", "PREROUTING")...)
	if err != nil && !psi && errors.Is(err, exec.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		markUnswept(cidr, !psi)
		return false, false, fmt.Errorf("read the nat redirects: %w: %s", err, firstLines(listing, 1))
	}
	_, checkErr := h.iptables(iptablesArgs(append([]string{"-t", "nat", "-C", "POSTROUTING"}, dnsNATRule(cidr)...)...)...)
	steps, redirected := psiphonNATSteps(cidr, poolRedirects(listing, cidr), checkErr == nil, port, psi)
	reached, wrote, err := runPoolSteps(h, steps)
	markUnswept(cidr, !psi && err != nil)
	return redirected || reached, wrote, err
}

// ReleasePool takes off a pool that no longer leaves through an exit what this code put
// there: its rules, redirects and DNS rewrite, and a table nothing looks up any more.
func ReleasePool(cidr string) error {
	if runtime.GOOS != "linux" || strings.TrimSpace(cidr) == "" {
		return nil
	}
	return releasePool(cidr, localPoolHost)
}

func releasePool(cidr string, h poolHost) error {
	rules, err := h.ip("rule", "show")
	if err != nil {
		return fmt.Errorf("read the routing rules: %w: %s", err, firstLines(rules, 1))
	}
	parsed := readRules(rules)
	steps := slices.Concat(
		mainRuleSteps(parsed, cidr, localSelector(cidr), 0, false),
		mainRuleSteps(parsed, cidr, dnsSelector, 0, false),
		strayRuleSteps(parsed, cidr, ipRule.panelRule),
	)
	if _, _, err := runPoolSteps(h, steps); err != nil {
		return err
	}
	_, _, err = steerPsiphonNAT(cidr, h, false, 0)
	return err
}

// The ports Xray takes redirected pools on, by exit, as the config it runs opens them; an
// exit missing here has its pools held. starting: the ports a config about to start opens.
var psiphonRedirects struct {
	sync.Mutex
	ports    map[string]int
	starting []int
	fitted   bool
}

func psiphonRedirectPort(exit string) int {
	psiphonRedirects.Lock()
	defer psiphonRedirects.Unlock()
	return psiphonRedirects.ports[strings.TrimSpace(exit)]
}

// guardHost is iptables and ip6tables: Xray's 0.0.0.0 listener takes IPv6 as well.
type guardHost struct {
	v4, v6 func(args ...string) (string, error)
}

var localGuardHost = guardHost{
	v4: func(args ...string) (string, error) { return runLocalOut("iptables", args...) },
	v6: func(args ...string) (string, error) { return runLocalOut("ip6tables", args...) },
}

// guardRule takes redirected connections to port, or drops every other one, so the port
// is no open proxy on this server's own addresses.
func guardRule(port int, redirected bool) []string {
	rule := []string{"-p", "tcp", "--dport", strconv.Itoa(port), "-m", "conntrack"}
	target := "DROP"
	if redirected {
		rule, target = append(rule, "--ctstate", "DNAT"), "ACCEPT"
	} else {
		rule = append(rule, "!", "--ctstate", "DNAT")
	}
	return append(rule, "-m", "comment", "--comment", psiphonMark, "-j", target)
}

// guardSteps fits the INPUT guards in listing to ports, ahead of any other rule: each port
// a drop, and where pools are redirected (IPv4) an accept. prune takes the rest away.
func guardSteps(listing string, ports []int, accept, prune bool) [][]string {
	type guard struct {
		port   string
		target string
	}
	wanted := func(g guard) bool {
		n, err := strconv.Atoi(g.port)
		return err == nil && slices.Contains(ports, n) && (g.target == "DROP" || (accept && g.target == "ACCEPT"))
	}
	seen := map[guard]bool{}
	var steps [][]string
	for _, l := range readNetfilter(listing, "INPUT") {
		if !l.marked() {
			continue
		}
		g := guard{port: l.value("--dport"), target: l.value("-j")}
		if wanted(g) && !seen[g] {
			seen[g] = true
			continue
		}
		if prune || seen[g] {
			steps = append(steps, l.deleteArgs(""))
		}
	}
	for _, port := range ports {
		if !seen[guard{port: strconv.Itoa(port), target: "DROP"}] {
			steps = append(steps, iptablesArgs(append([]string{"-I", "INPUT"}, guardRule(port, false)...)...))
		}
		if accept && !seen[guard{port: strconv.Itoa(port), target: "ACCEPT"}] {
			steps = append(steps, iptablesArgs(append([]string{"-I", "INPUT"}, guardRule(port, true)...)...))
		}
	}
	return steps
}

// fitGuards brings both families' INPUT guards to ports. A family that cannot be read
// while no port needs a guard holds none of ours: a box without ip6tables is no error.
func fitGuards(h guardHost, ports []int, prune bool) (wrote bool, err error) {
	var errs []error
	for _, family := range []struct {
		name   string
		run    func(args ...string) (string, error)
		accept bool
	}{{"iptables", h.v4, true}, {"ip6tables", h.v6, false}} {
		listing, listErr := family.run(iptablesArgs("-S", "INPUT")...)
		if listErr != nil && len(ports) > 0 {
			errs = append(errs, fmt.Errorf("%s -S INPUT: %w: %s", family.name, listErr, firstLines(listing, 1)))
		}
		if listErr != nil {
			continue
		}
		for _, args := range guardSteps(listing, ports, family.accept, prune) {
			if out, runErr := family.run(args...); runErr != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w: %s", family.name, strings.Join(args, " "), runErr, firstLines(out, 1)))
				continue
			}
			wrote = true
		}
	}
	return wrote, errors.Join(errs...)
}

func redirectPortList(redirects map[string]int) []int {
	ports := slices.Collect(maps.Values(redirects))
	slices.Sort(ports)
	return slices.Compact(ports)
}

// guardedPorts is every port a guard stays on: the running config's, and those of one
// about to start, which the ten-second pass would otherwise take before it runs.
func guardedPorts() []int {
	ports := slices.Concat(redirectPortList(psiphonRedirects.ports), psiphonRedirects.starting)
	slices.Sort(ports)
	return slices.Compact(ports)
}

// GuardPsiphonRedirects guards the ports a config about to start opens, before Xray
// listens there; no guard goes, which waits for that config to run.
func GuardPsiphonRedirects(redirects map[string]int) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return guardPsiphonRedirects(redirects, localGuardHost)
}

func guardPsiphonRedirects(redirects map[string]int, h guardHost) error {
	if len(redirects) == 0 {
		return nil
	}
	r := &psiphonRedirects
	r.Lock()
	defer r.Unlock()
	r.starting = redirectPortList(redirects)
	_, err := fitGuards(h, r.starting, false)
	return err
}

// SetPsiphonRedirects records the ports the config Xray now runs takes each Psiphon exit's
// pools on and fits the guards to those alone; changed: a pool may now go elsewhere.
func SetPsiphonRedirects(redirects map[string]int) (changed bool, err error) {
	return setPsiphonRedirects(redirects, localGuardHost, runtime.GOOS == "linux")
}

func setPsiphonRedirects(redirects map[string]int, h guardHost, guard bool) (changed bool, err error) {
	r := &psiphonRedirects
	r.Lock()
	defer r.Unlock()
	changed = !maps.Equal(r.ports, redirects)
	idle := r.fitted && len(r.ports) == 0 && len(r.starting) == 0 && len(redirects) == 0
	r.ports, r.starting = maps.Clone(redirects), nil
	if !guard || idle {
		return changed, nil
	}
	r.fitted = true
	_, err = fitGuards(h, redirectPortList(r.ports), true)
	return changed, err
}

// ReassertPsiphonGuards puts back a guard something flushed, for the ten-second pass.
func ReassertPsiphonGuards() (wrote bool, err error) {
	if runtime.GOOS != "linux" {
		return false, nil
	}
	return reassertPsiphonGuards(localGuardHost)
}

func reassertPsiphonGuards(h guardHost) (wrote bool, err error) {
	psiphonRedirects.Lock()
	defer psiphonRedirects.Unlock()
	ports := guardedPorts()
	if len(ports) == 0 {
		return false, nil
	}
	return fitGuards(h, ports, true)
}
