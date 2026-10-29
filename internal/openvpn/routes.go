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

// An inbound only becomes a country once its pool leaves through that country: the
// server hands out an address, and steering the pool is the host's job, as for cisco.

const commandTimeout = 10 * time.Second

// routeTableFor is the table an inbound's pool is steered into: its own, which no cisco
// location shares (relay.OpenVPNPoolTable). 7600 + id%300 once met cisco's at id 38.
func routeTableFor(id int) int { return relay.OpenVPNPoolTable(id) }

var run = func(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

var runOut = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// What touches the host's routes and NAT, run and runOut too, is a variable so a
// test can stand a fake host in; onLinux is what lets any of it run.
var (
	onLinux        = runtime.GOOS == "linux"
	steerPool      = relay.SteerPool
	reassertPool   = relay.ReassertPool
	releasePool    = relay.ReleasePool
	reassertGuards = relay.ReassertPsiphonGuards
	ensureExitSNAT = relay.EnsureExitSNAT
	removeExitSNAT = relay.RemoveExitSNAT
)

func natRuleExists(cidr string) bool {
	return run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE") == nil
}

// ensureNAT translates the pool onto the host's own address. Without it a
// client connects, gets a lease and reaches nothing at all — which reads as
// "the VPN is broken" rather than "the host is not forwarding".
func ensureNAT(cidr string) {
	if !onLinux || cidr == "" {
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
	if !onLinux || cidr == "" {
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
	return err == nil && relay.PoolRuleIn(out, cidr, table)
}

// ensureExit steers one inbound's pool.
//
// With no exit the pool leaves through this host and is masqueraded here.
// With an exit it must NOT be masqueraded here: the rule names no outgoing
// interface, so it would rewrite the packets heading into the tunnel too, and
// the server at the far end would have no address left to answer.
func ensureExit(cidr, exit string, table int) {
	if !onLinux || cidr == "" {
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
	through, _, err := steerPool(relay.PoolExit{CIDR: cidr, Exit: exit, Table: table})
	if err != nil {
		logger.Warningf("openvpn: could not steer %s into table %d: %v", cidr, table, err)
		return
	}
	// The default pool, 10.1.x, is outside what a relay exit accepts.
	if err := ensureExitSNAT(cidr, exit); err != nil {
		logger.Warningf("openvpn: %s reaches %s but cannot be answered: %v", cidr, exit, err)
	}
	if !through {
		logger.Warningf("openvpn: %s is held closed: %q is missing or down, and clients on this inbound reach nothing until it is back", cidr, exit)
		return
	}
	logger.Infof("openvpn: %s now leaves through %s", cidr, exit)
}

// ReassertRoutes puts back what a recreated device (relay.Up remakes every wgrN) or a
// flushed iptables took from the pools, without restarting anything.
func (m *Manager) ReassertRoutes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.procs {
		exit := strings.TrimSpace(cur.exit)
		// No exit is masqueraded here; a table would cut the inbound off.
		if exit == "" {
			continue
		}
		through, wrote, err := reassertPool(relay.PoolExit{CIDR: cur.cidr, Exit: exit, Table: cur.table})
		switch {
		case err != nil:
			logger.Debugf("openvpn: could not reassert %s on %s: %v", cur.cidr, exit, err)
		case wrote && through:
			logger.Infof("openvpn: %s leaves through %s again", cur.cidr, exit)
		case wrote:
			logger.Infof("openvpn: %s is held closed until %s is back", cur.cidr, exit)
		}
	}
	if wrote, err := reassertGuards(); err != nil {
		logger.Debugf("openvpn: could not reassert the Psiphon redirect guards: %v", err)
	} else if wrote {
		logger.Info("openvpn: the Psiphon redirect ports are guarded again")
	}
}

// removeExit takes a pool back off the wire. A rule left behind keeps sending
// an address range into a tunnel nobody serves, and the next inbound handed
// that range would vanish.
func removeExit(cidr string, table int) {
	if !onLinux || cidr == "" {
		return
	}
	removeExitSNAT(cidr)
	if err := releasePool(cidr); err != nil {
		logger.Warningf("openvpn: could not take every rule off %s: %v", cidr, err)
	}
	t := strconv.Itoa(table)
	for ipRuleExists(cidr, table) {
		if err := run("ip", "rule", "del", "from", cidr, "lookup", t); err != nil {
			break
		}
	}
	_ = run("ip", "route", "flush", "table", t)
}
