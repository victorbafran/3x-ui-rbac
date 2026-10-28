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

// PsiphonExitPrefix starts an exit a Psiphon client carries: for cisco and OpenVPN
// an Xray TUN of that very name, not a relay's WireGuard tunnel.
const PsiphonExitPrefix = "psi-"

// IsPsiphonExit reports whether an exit is a Psiphon country rather than a relay.
func IsPsiphonExit(iface string) bool {
	return strings.HasPrefix(strings.TrimSpace(iface), PsiphonExitPrefix)
}

// poolHoldMetric puts the blackhole behind the exit's own route, which has none.
const poolHoldMetric = "4000"

// PoolExit is one pool sent out of an exit device through its own table and rule.
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

// PoolRuleIn reports whether `ip rule show` output holds the rule sending cidr into
// table. Its priority is the table's number, which a table named in rt_tables keeps.
func PoolRuleIn(out, cidr string, table int) bool {
	n := strconv.Itoa(table)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		from, lookup := slices.Index(f, "from"), slices.Index(f, "lookup")
		if from < 0 || from+1 >= len(f) || f[from+1] != cidr || lookup < 0 || lookup+1 >= len(f) {
			continue
		}
		if f[lookup+1] == n {
			return true
		}
		if _, numeric := strconv.Atoi(f[lookup+1]); numeric != nil && f[0] == n+":" {
			return true
		}
	}
	return false
}

// poolStep is one ip command. A step that may fail writes a route on a device that
// can vanish between the read and the write, which leaves the pool held, not open.
type poolStep struct {
	args    []string
	mayFail bool
	toExit  bool
}

// poolSteps is what brings a pool's table and rule to what p needs from what is
// there now. The blackhole goes in first, so the rule never points at an empty table.
// A reassert leaves another exit's route where it is: what a start did not take out
// is a table two pools share, and taking it every tick would flip both of them.
func poolSteps(p PoolExit, have poolTable, ruled, deviceUp, reassert bool) []poolStep {
	table := strconv.Itoa(p.Table)
	var steps []poolStep
	if !have.held {
		steps = append(steps, poolStep{args: []string{"route", "replace", "blackhole", "default", "table", table, "metric", poolHoldMetric}})
	}
	other := slices.ContainsFunc(have.devs, func(dev string) bool { return dev != p.Exit })
	// A route left to a previous exit would carry the pool to the wrong country
	// while its new device is still missing.
	for _, dev := range have.devs {
		if dev != p.Exit && !reassert {
			steps = append(steps, poolStep{args: []string{"route", "del", "default", "dev", dev, "table", table}, mayFail: true})
		}
	}
	if deviceUp && !slices.Contains(have.devs, p.Exit) && !(reassert && other) {
		steps = append(steps, poolStep{args: []string{"route", "replace", "default", "dev", p.Exit, "table", table}, mayFail: true, toExit: true})
	}
	if !ruled {
		steps = append(steps, poolStep{args: []string{"rule", "add", "from", p.CIDR, "lookup", table, "priority", table}})
	}
	return steps
}

// poolHost is the part of the system a pool's steering reads and writes.
type poolHost struct {
	ip       func(args ...string) (string, error)
	deviceUp func(name string) bool
}

var localPoolHost = poolHost{
	ip: func(args ...string) (string, error) { return runLocalOut("ip", args...) },
	deviceUp: func(name string) bool {
		ifc, err := net.InterfaceByName(name)
		return err == nil && ifc.Flags&net.FlagUp != 0
	},
}

// SteerPool writes whatever p's table and rule are missing, so a reconcile can repeat
// it, and takes out a route a previous exit left. through is false while the exit's
// device is gone.
func SteerPool(p PoolExit) (through, wrote bool, err error) {
	if runtime.GOOS != "linux" {
		return false, false, nil
	}
	return steerPool(p, localPoolHost, false)
}

// ReassertPool is SteerPool for the periodic pass: it writes only what is missing and
// leaves a route to another exit alone, which only a table two pools share keeps.
func ReassertPool(p PoolExit) (through, wrote bool, err error) {
	if runtime.GOOS != "linux" {
		return false, false, nil
	}
	return steerPool(p, localPoolHost, true)
}

// missingTable is how `ip route show` answers for a table nothing has written since
// boot (exit status 2); the blackhole's write is what creates it.
const missingTable = "FIB table does not exist"

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
	through = slices.Contains(have.devs, p.Exit)
	for _, s := range poolSteps(p, have, PoolRuleIn(rules, p.CIDR, p.Table), h.deviceUp(p.Exit), reassert) {
		out, runErr := h.ip(s.args...)
		if runErr == nil {
			wrote = true
			through = through || s.toExit
			continue
		}
		if !s.mayFail {
			return false, wrote, fmt.Errorf("ip %s: %w: %s", strings.Join(s.args, " "), runErr, firstLines(out, 1))
		}
	}
	if !through && reassert && len(have.devs) > 0 {
		return false, wrote, fmt.Errorf("table %s leaves through %s, another pool's exit: the table numbers of a cisco location and an OpenVPN inbound collide", table, strings.Join(have.devs, ", "))
	}
	return through, wrote, nil
}
