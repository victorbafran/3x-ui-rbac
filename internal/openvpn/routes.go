package openvpn

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// An inbound only becomes a country once its pool actually leaves through
// that country. The server hands the client an address and stops there;
// steering the pool is the host's job.
//
// internal/ocserv/routes.go does the same for cisco locations. The two are
// kept apart deliberately for now: that one is serving live customers and its
// table numbers are already allocated, so sharing the code means renumbering
// something that is running. The numbering here is chosen so the two can
// never collide.

const commandTimeout = 10 * time.Second

// routeTableBase sits above the block cisco locations use (7000 upward, one
// per location). An OpenVPN inbound takes one table each from 7600, which is
// clear of both that range and of anything an operator is likely to have.
const routeTableBase = 7600

func routeTableFor(id int) int {
	if id < 0 {
		id = -id
	}
	return routeTableBase + id%300
}

func run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func runOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func natRuleExists(cidr string) bool {
	return run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE") == nil
}

// ensureNAT translates the pool onto the host's own address. Without it a
// client connects, gets a lease and reaches nothing at all — which reads as
// "the VPN is broken" rather than "the host is not forwarding".
func ensureNAT(cidr string) {
	if runtime.GOOS != "linux" || cidr == "" {
		return
	}
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		logger.Warningf("openvpn: could not enable ip_forward: %v", err)
	}
	if natRuleExists(cidr) {
		return
	}
	if err := run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE"); err != nil {
		logger.Warningf("openvpn: could not add the NAT rule for %s (clients will connect but reach nothing): %v", cidr, err)
	}
}

func removeNAT(cidr string) {
	if runtime.GOOS != "linux" || cidr == "" {
		return
	}
	for natRuleExists(cidr) {
		if err := run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE"); err != nil {
			return
		}
	}
}

func ipRuleExists(cidr string, table int) bool {
	out, err := runOut("ip", "rule", "show")
	if err != nil {
		return false
	}
	want := "from " + cidr + " lookup " + strconv.Itoa(table)
	named := "from " + cidr + " lookup "
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return true
		}
		// A table with a name in rt_tables prints the name, not the number,
		// so matching the number alone would add the rule again every time.
		if strings.Contains(line, named) && strings.Contains(line, "priority "+strconv.Itoa(table)) {
			return true
		}
	}
	return false
}

// ensureExit steers one inbound's pool.
//
// With no exit the pool leaves through this host and is masqueraded here.
// With an exit it must NOT be masqueraded here: the rule names no outgoing
// interface, so it would rewrite the packets heading into the tunnel too, and
// the server at the far end would have no address left to answer.
func ensureExit(cidr, exit string, table int) {
	if runtime.GOOS != "linux" || cidr == "" {
		return
	}
	exit = strings.TrimSpace(exit)
	if exit == "" {
		removeExit(cidr, table)
		ensureNAT(cidr)
		return
	}

	removeNAT(cidr)
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		logger.Warningf("openvpn: could not enable ip_forward: %v", err)
	}
	t := strconv.Itoa(table)
	// replace, not add: an exit the operator has just changed should take
	// effect rather than collide with the route already in the table.
	if err := run("ip", "route", "replace", "default", "dev", exit, "table", t); err != nil {
		logger.Warningf("openvpn: %s cannot leave through %q — the interface is missing or down, and clients on this inbound will reach nothing: %v", cidr, exit, err)
		return
	}
	if !ipRuleExists(cidr, table) {
		if err := run("ip", "rule", "add", "from", cidr, "lookup", t, "priority", t); err != nil {
			logger.Warningf("openvpn: could not steer %s into table %s: %v", cidr, t, err)
			return
		}
	}
	// The default pool, 10.1.x, is outside what an exit accepts.
	if err := relay.EnsureExitSNAT(cidr, exit); err != nil {
		logger.Warningf("openvpn: %s reaches %s but cannot be answered: %v", cidr, exit, err)
	}
	logger.Infof("openvpn: %s now leaves through %s", cidr, exit)
}

// removeExit takes a pool back off the wire. A rule left behind keeps sending
// an address range into a tunnel nobody serves, and the next inbound handed
// that range would vanish.
func removeExit(cidr string, table int) {
	if runtime.GOOS != "linux" || cidr == "" {
		return
	}
	relay.RemoveExitSNAT(cidr)
	t := strconv.Itoa(table)
	for ipRuleExists(cidr, table) {
		if err := run("ip", "rule", "del", "from", cidr, "lookup", t); err != nil {
			break
		}
	}
	_ = run("ip", "route", "flush", "table", t)
}
