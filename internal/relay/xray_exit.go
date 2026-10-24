package relay

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Xray leaves through an exit by sending from its own address inside exitPools,
// which the exit accepts, and a rule on that source steers it like cisco's pools.

const (
	xrayExitTableBase = 60000
	// Below main's 32766, or main's default route would answer first; clear of
	// cisco's 7000 and OpenVPN's 7600 blocks.
	xrayExitPriorityBase = 6000
)

// XrayExitSource is the address an Xray outbound sends from to leave through
// exit id: 10.255.255.id, the top of exitPools, which allows 254 exits.
func XrayExitSource(id int) (string, error) {
	if id < 1 || id > 254 {
		return "", fmt.Errorf("exit %d cannot carry Xray traffic: only exits 1 to 254 have a source address", id)
	}
	return fmt.Sprintf("10.255.255.%d", id), nil
}

// InterfaceID is the relay id behind an interface name InterfaceName made.
func InterfaceID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(name), "wgr")
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(rest)
	if err != nil || id < 1 || strconv.Itoa(id) != rest {
		return 0, false
	}
	return id, true
}

type ipCommand []string

// xrayExitCommands puts exit id's source address, table and rule in place;
// addRule is false when the rule is already there, so it is never stacked.
func xrayExitCommands(id int, addRule bool) ([]ipCommand, error) {
	src, err := XrayExitSource(id)
	if err != nil {
		return nil, err
	}
	iface := InterfaceName(id)
	table := strconv.Itoa(xrayExitTableBase + id)
	cmds := []ipCommand{
		{"ip", "addr", "replace", src + "/32", "dev", iface},
		{"ip", "route", "replace", "default", "dev", iface, "table", table},
	}
	if addRule {
		cmds = append(cmds, ipCommand{"ip", "rule", "add", "from", src, "lookup", table, "priority", strconv.Itoa(xrayExitPriorityBase + id)})
	}
	return cmds, nil
}

// xrayExitRuleIn reports whether `ip rule show` output holds exit id's rule.
func xrayExitRuleIn(out string, id int) bool {
	src, err := XrayExitSource(id)
	if err != nil {
		return false
	}
	prefix := strconv.Itoa(xrayExitPriorityBase+id) + ":"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) && strings.Contains(line, "from "+src+" ") {
			return true
		}
	}
	return false
}

// EnsureXrayExit lets Xray send through exit id. Safe to repeat: a reconcile
// after wg-quick restarted the interface puts back the address it took away.
func EnsureXrayExit(id int) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	rules, err := runLocalOut("ip", "rule", "show")
	if err != nil {
		return fmt.Errorf("read the routing rules: %w", err)
	}
	cmds, err := xrayExitCommands(id, !xrayExitRuleIn(rules, id))
	if err != nil {
		return err
	}
	for _, c := range cmds {
		if out, err := runLocalOut(c[0], c[1:]...); err != nil {
			return fmt.Errorf("%s: %w: %s", strings.Join(c, " "), err, firstLines(out, 1))
		}
	}
	return nil
}

// RemoveXrayExit takes exit id's rule and table away; the address goes with
// the interface.
func RemoveXrayExit(id int) {
	if runtime.GOOS != "linux" {
		return
	}
	src, err := XrayExitSource(id)
	if err != nil {
		return
	}
	table := strconv.Itoa(xrayExitTableBase + id)
	for range 8 {
		out, err := runLocalOut("ip", "rule", "show")
		if err != nil || !xrayExitRuleIn(out, id) {
			break
		}
		if runLocal("ip", "rule", "del", "from", src, "lookup", table) != nil {
			break
		}
	}
	_ = runLocal("ip", "route", "flush", "table", table)
}

// LocalInterfaceIDs lists the relay ids this panel has a tunnel config for,
// so a reconcile can take down any whose relay no longer exists.
func LocalInterfaceIDs() []int {
	entries, err := os.ReadDir(localDir())
	if err != nil {
		return nil
	}
	var ids []int
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".conf")
		if !ok {
			continue
		}
		if id, ok := InterfaceID(name); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// DownOrphan takes down the panel's end of a tunnel whose relay is gone. The
// key stays: an older database restored over a newer one leaves relays out.
func DownOrphan(id int) {
	downLocal(Server{Id: id})
	RemoveXrayExit(id)
}
