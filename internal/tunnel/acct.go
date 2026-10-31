package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Backhaul's own counter needs its web port, which binds every address, so the panel
// counts a Backhaul front's tunnel in its own netfilter: count-only rules in a chain of their own.
const (
	acctChain = "X_UI_FRONT_ACCT"
	acctMark  = "3x-ui:front-acct"
)

// acctParents are the chains that jump to acctChain: the front's bytes to the panel, and back.
var acctParents = []string{"INPUT", "OUTPUT"}

// acctArgs waits for the lock: the Psiphon guards and the cisco/OpenVPN NAT write these tables too.
func acctArgs(args ...string) []string { return append([]string{"-w", "5"}, args...) }

// acctRule counts one direction of one Backhaul leg: out is what the panel sends the
// front, the customers' download (traffic_out); in is what comes back, their upload.
type acctRule struct {
	id    int
	leg   string
	out   bool
	proto string
	addr  netip.Addr
	port  int
}

// reading names the rule's counter, apart from the other direction's: each is
// recreated on its own, and one reset must not count the other from zero.
func (r acctRule) reading() string {
	if r.out {
		return r.leg + ":out"
	}
	return r.leg + ":in"
}

func (r acctRule) flags() (addr, port string) {
	if r.out {
		return "-d", "--dport"
	}
	return "-s", "--sport"
}

// spec is the rule as `iptables -S` prints it back, the way fit recognises it.
func (r acctRule) spec() []string {
	addr, port := r.flags()
	return []string{
		addr, netip.PrefixFrom(r.addr, r.addr.BitLen()).String(), "-p", r.proto, "-m", r.proto, port, strconv.Itoa(r.port),
		"-m", "comment", "--comment", acctMark + ":" + r.reading(), "-j", "RETURN",
	}
}

func (r acctRule) matches(l acctLine) bool {
	addr, port := r.flags()
	host, err := netip.ParsePrefix(l.value(addr))
	return err == nil && host.Bits() == r.addr.BitLen() && host.Addr() == r.addr &&
		l.value("-p") == r.proto && l.value(port) == strconv.Itoa(r.port) &&
		l.value("--comment") == acctMark+":"+r.reading() && l.value("-j") == "RETURN"
}

// acctRules is every rule the Backhaul fronts among fronts need, by family. A front
// given by name has none: which address it dials is the resolver's, as in Established.
func acctRules(fronts []Pair) (v4, v6 []acctRule) {
	for _, front := range fronts {
		if front.Tool.norm() != ToolBackhaul {
			continue
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(front.FrontAddress))
		if err != nil {
			continue
		}
		for _, leg := range Legs(front) {
			proto := "udp"
			if tcpCarried(leg.Transport) {
				proto = "tcp"
			}
			for _, out := range []bool{true, false} {
				rule := acctRule{id: front.Id, leg: leg.Key(), out: out, proto: proto, addr: addr, port: leg.TunnelPort}
				if addr.Is4() {
					v4 = append(v4, rule)
				} else {
					v6 = append(v6, rule)
				}
			}
		}
	}
	return v4, v6
}

// acctLine is one rule of an `iptables -S` listing, quotes taken off.
type acctLine []string

func readAcctLines(listing, chain string) []acctLine {
	var out []acctLine
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

func (l acctLine) value(flag string) string {
	if i := slices.Index(l, flag); i >= 0 && i+1 < len(l) {
		return l[i+1]
	}
	return ""
}

func (l acctLine) deleteArgs() []string { return acctArgs(append([]string{"-D"}, l[1:]...)...) }

// bytes is the byte counter `iptables -S -v` prints after -c, behind the packets.
func (l acctLine) bytes() (int64, bool) {
	i := slices.Index(l, "-c")
	if i < 0 || i+2 >= len(l) {
		return 0, false
	}
	n, err := strconv.ParseInt(l[i+2], 10, 64)
	return n, err == nil
}

// counted is what rule r counted in lines: a rule twice over adds both, as the first
// returns and the second keeps what it had. A rule of another address is not r.
func (r acctRule) counted(lines []acctLine) (sum int64, found bool) {
	for _, l := range lines {
		if n, ok := l.bytes(); ok && r.matches(l) {
			sum, found = sum+n, true
		}
	}
	return sum, found
}

// acctChainSteps fits the chain in listing to want: each wanted rule once, kept with
// its counters where it already stands, and anything else in the chain taken out.
func acctChainSteps(listing string, want []acctRule) [][]string {
	kept := make([]bool, len(want))
	var drop [][]string
	for _, l := range readAcctLines(listing, acctChain) {
		i := slices.IndexFunc(want, func(r acctRule) bool { return r.matches(l) })
		if i >= 0 && !kept[i] {
			kept[i] = true
			continue
		}
		drop = append(drop, l.deleteArgs())
	}
	var steps [][]string
	for i, r := range want {
		if !kept[i] {
			steps = append(steps, acctArgs(append([]string{"-A", acctChain}, r.spec()...)...))
		}
	}
	return append(steps, drop...)
}

// acctJumpSteps leaves parent one jump to the chain, put at the top when missing so
// no earlier accept skips it, or none; a second jump would count every byte twice.
func acctJumpSteps(listing, parent string, keep bool) [][]string {
	var jumps []acctLine
	for _, l := range readAcctLines(listing, parent) {
		if l.value("-j") == acctChain {
			jumps = append(jumps, l)
		}
	}
	if keep && len(jumps) == 1 {
		return nil
	}
	// -D takes the first rule it matches, so a doubled jump is taken off whole and put back once.
	var steps [][]string
	for _, l := range jumps {
		steps = append(steps, l.deleteArgs())
	}
	if keep {
		steps = append(steps, acctArgs("-I", parent, "1", "-m", "comment", "--comment", acctMark, "-j", acctChain))
	}
	return steps
}

type tablesRun func(args ...string) (string, error)

func noSuchChain(err error) bool {
	return strings.Contains(err.Error(), "No chain/target/match by that name")
}

// fitAcctFamily brings one family's chain and jumps to want; holding is whether a chain is left
// to read. With none wanted, a chain it cannot read (no binary, no NET_ADMIN) is none to take off.
func fitAcctFamily(run tablesRun, want []acctRule) (holding bool, err error) {
	listing, err := run(acctArgs("-S", acctChain)...)
	if err != nil && len(want) == 0 {
		return false, nil
	}
	if err != nil && !noSuchChain(err) {
		return true, fmt.Errorf("read %s: %w", acctChain, err)
	}
	if err != nil {
		if _, err := run(acctArgs("-N", acctChain)...); err != nil {
			return false, fmt.Errorf("create %s: %w", acctChain, err)
		}
	}

	var steps [][]string
	var errs []error
	if len(want) > 0 {
		steps = acctChainSteps(listing, want)
	}
	for _, parent := range acctParents {
		jumps, err := run(acctArgs("-S", parent)...)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", parent, err))
			continue
		}
		steps = append(steps, acctJumpSteps(jumps, parent, len(want) > 0)...)
	}
	// Unhooked first: a chain still jumped to cannot be deleted.
	if len(want) == 0 && len(errs) == 0 {
		steps = append(steps, acctArgs("-F", acctChain), acctArgs("-X", acctChain))
	}
	for _, args := range steps {
		if _, err := run(args...); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", strings.Join(args[2:], " "), err))
		}
	}
	return len(want) > 0, errors.Join(errs...)
}

// acctTables is the panel's counting in both families. One lock covers fits and reads,
// so a read never meets a chain halfway through being fitted.
type acctTables struct {
	mu sync.Mutex
	v4 tablesRun
	v6 tablesRun
	// holds: the family has rules to read; clean: it was found holding none of ours
	// with none wanted, so a panel with no Backhaul front runs no iptables at all.
	holds, clean [2]bool
}

func (t *acctTables) families() [2]tablesRun { return [2]tablesRun{t.v4, t.v6} }

var familyNames = [2]string{"iptables", "ip6tables"}

func (t *acctTables) fit(fronts []Pair) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	v4, v6 := acctRules(fronts)
	wants := [2][]acctRule{v4, v6}
	var errs []error
	for i, run := range t.families() {
		if len(wants[i]) == 0 && t.clean[i] {
			continue
		}
		holding, err := fitAcctFamily(run, wants[i])
		t.holds[i] = holding
		t.clean[i] = len(wants[i]) == 0 && err == nil
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", familyNames[i], err))
		}
	}
	return errors.Join(errs...)
}

// read is each front's readings, only from a rule it wants now: until the next fit the
// chain may still count a front's old address, or, mid-import, the other box's fronts.
func (t *acctTables) read(fronts []Pair, now time.Time) (map[int]map[string]LegMetrics, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v4, v6 := acctRules(fronts)
	wants := [2][]acctRule{v4, v6}
	out := map[int]map[string]LegMetrics{}
	var errs []error
	for i, run := range t.families() {
		if !t.holds[i] || len(wants[i]) == 0 {
			continue
		}
		listing, err := run(acctArgs("-S", acctChain, "-v")...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", familyNames[i], err))
			continue
		}
		lines := readAcctLines(listing, acctChain)
		for _, r := range wants[i] {
			n, ok := r.counted(lines)
			if !ok {
				continue
			}
			m := LegMetrics{Taken: now}
			if r.out {
				m.BytesOut = n
			} else {
				m.BytesIn = n
			}
			if out[r.id] == nil {
				out[r.id] = map[string]LegMetrics{}
			}
			out[r.id][r.reading()] = m
		}
	}
	return out, errors.Join(errs...)
}

// localTables runs bin with its error carrying what bin said; stdout alone is the
// listing, so a warning on stderr never reads as a rule.
func localTables(bin string) tablesRun {
	return func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
}

var localAcct = &acctTables{v4: localTables("iptables"), v6: localTables("ip6tables")}

// FitFrontAcct has the panel count the tunnels of the Backhaul fronts among fronts, and
// no others: the rules of a front gone, disabled or on BackPack are taken off.
func FitFrontAcct(fronts []Pair) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return localAcct.fit(fronts)
}

// ReadFrontAcct is the readings of the Backhaul fronts among fronts by relay id, under a key per
// leg and direction, taken now; tunnel bytes, so TCP/IP, TLS and mux framing included.
func ReadFrontAcct(fronts []Pair, now time.Time) (map[int]map[string]LegMetrics, error) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	return localAcct.read(fronts, now)
}
