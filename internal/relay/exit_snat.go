package relay

import (
	"fmt"
	"net"
	"runtime"
	"strings"
)

// An exit's WireGuard peer and masquerade accept exitPools and nothing else, so
// a pool outside it, OpenVPN's 10.1.x for one, is rewritten on its way in.

var exitPoolsNet = func() *net.IPNet {
	_, n, err := net.ParseCIDR(exitPools)
	if err != nil {
		panic(err)
	}
	return n
}()

// PoolInsideExitPools reports whether an exit accepts a pool as it is. ParseCIDR
// aligns the start to the prefix, so a pool wider than exitPools starts outside.
func PoolInsideExitPools(cidr string) bool {
	_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
	return err == nil && exitPoolsNet.Contains(n.IP)
}

// exitSNATArgs is the iptables rule rewriting cidr to exit iface's source;
// action is -C, -A or -D.
func exitSNATArgs(action, cidr, iface string) ([]string, error) {
	id, ok := InterfaceID(iface)
	if !ok {
		return nil, fmt.Errorf("%q is not one of this panel's exits, so it has no source address", iface)
	}
	src, err := XrayExitSource(id)
	if err != nil {
		return nil, err
	}
	return []string{"-t", "nat", action, "POSTROUTING", "-s", cidr, "-o", iface, "-j", "SNAT", "--to-source", src}, nil
}

// exitSNATRulesFor picks, out of `iptables -t nat -S POSTROUTING`, the exit
// rewrites for cidr, as delete arguments, whichever exit they name.
func exitSNATRulesFor(listing, cidr string) [][]string {
	var out [][]string
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "-A" || f[1] != "POSTROUTING" {
			continue
		}
		joined := " " + strings.Join(f, " ") + " "
		if strings.Contains(joined, " -s "+cidr+" ") && strings.Contains(joined, " -j SNAT ") &&
			strings.Contains(joined, " --to-source 10.255.255.") {
			out = append(out, append([]string{"-t", "nat", "-D"}, f[1:]...))
		}
	}
	return out
}

// exitNeedsSNAT: an exit's WireGuard peer takes exitPools only, while a Psiphon
// exit is Xray's own TUN, which takes a pool from any source.
func exitNeedsSNAT(cidr, iface string) bool {
	return !IsPsiphonExit(iface) && !PoolInsideExitPools(cidr)
}

// EnsureExitSNAT rewrites a pool outside exitPools to its exit's source on the
// way in; a pool needing none gets none, and a stale rewrite to another exit goes.
func EnsureExitSNAT(cidr, iface string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return ensureExitSNAT(cidr, iface, localIPTables)
}

// ipTables runs iptables with args and returns what it printed.
type ipTables func(args ...string) (string, error)

var localIPTables ipTables = func(args ...string) (string, error) { return runLocalOut("iptables", args...) }

func ensureExitSNAT(cidr, iface string, ipt ipTables) error {
	if !exitNeedsSNAT(cidr, iface) {
		removeExitSNAT(cidr, ipt)
		return nil
	}
	check, err := exitSNATArgs("-C", cidr, iface)
	if err != nil {
		return err
	}
	if _, err := ipt(check...); err == nil {
		return nil
	}
	removeExitSNAT(cidr, ipt)
	add, _ := exitSNATArgs("-A", cidr, iface)
	if out, err := ipt(add...); err != nil {
		return fmt.Errorf("rewrite %s into %s: %w: %s", cidr, iface, err, firstLines(out, 1))
	}
	return nil
}

// RemoveExitSNAT takes away every exit rewrite of cidr.
func RemoveExitSNAT(cidr string) {
	if runtime.GOOS != "linux" {
		return
	}
	removeExitSNAT(cidr, localIPTables)
}

func removeExitSNAT(cidr string, ipt ipTables) {
	if strings.TrimSpace(cidr) == "" {
		return
	}
	listing, err := ipt("-t", "nat", "-S", "POSTROUTING")
	if err != nil {
		return
	}
	for _, args := range exitSNATRulesFor(listing, cidr) {
		_, _ = ipt(args...)
	}
}
