package ocserv

import (
	"context"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// A location only becomes a country once its pool actually leaves through that
// country. ocserv hands the client an address out of the right pool and stops
// there; steering that pool is the host's job, and it is done here.
//
// Every location gets a routing table of its own and a single rule that sends
// its pool into it. The host's own default route is never touched: a panel
// that rewrites the route its own SSH session came in on is a panel that
// locks the operator out of the server.

// locationRoute is everything needed to put one country's pool on the wire,
// and to take it back off again later.
type locationRoute struct {
	// CIDR is the pool this country hands out.
	CIDR string
	// Exit is the interface the pool leaves through. Empty means this host.
	Exit string
	// Table is the routing table this pool is steered into.
	Table int
}

var runOut = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), natTimeout)
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
	ensureExitSNAT = relay.EnsureExitSNAT
	removeExitSNAT = relay.RemoveExitSNAT
)

// ipRuleExists reports whether the rule steering this pool is already in
// place, so a reconcile does not stack a second copy of it every time.
func ipRuleExists(cidr string, table int) bool {
	out, err := runOut("ip", "rule", "show")
	return err == nil && relay.PoolRuleIn(out, cidr, table)
}

// ensureLocationRoute puts one country's pool on the right path.
//
// With no exit the pool leaves through this host and is masqueraded here, the
// same as an inbound's own pool. With an exit it must NOT be masqueraded here:
// the rule nat.go writes has no outgoing interface, so it would rewrite the
// packets heading into the tunnel too, and the server at the far end would
// have nothing left to route the answers back to.
func ensureLocationRoute(r locationRoute) {
	if !onLinux || r.CIDR == "" {
		return
	}
	exit := strings.TrimSpace(r.Exit)
	if exit == "" {
		removeLocationRoute(locationRoute{CIDR: r.CIDR, Exit: "x", Table: r.Table}, nil)
		ensureNAT(r.CIDR)
		return
	}

	removeNAT(r.CIDR)
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		logger.Warningf("ocserv: could not enable ip_forward: %v", err)
	}
	through, _, err := steerPool(r.poolExit())
	if err != nil {
		logger.Warningf("ocserv: could not steer %s into table %d: %v", r.CIDR, r.Table, err)
		return
	}
	// A network the operator typed for a country can sit outside what exits accept.
	if err := ensureExitSNAT(r.CIDR, exit); err != nil {
		logger.Warningf("ocserv: %s reaches %s but cannot be answered: %v", r.CIDR, exit, err)
	}
	if !through {
		logger.Warningf("ocserv: %s is held closed: %q is missing or down, and clients who pick that location reach nothing until it is back", r.CIDR, exit)
		return
	}
	logger.Infof("ocserv: %s now leaves through %s", r.CIDR, exit)
}

func (r locationRoute) poolExit() relay.PoolExit {
	return relay.PoolExit{CIDR: r.CIDR, Exit: strings.TrimSpace(r.Exit), Table: r.Table}
}

// ReassertRoutes puts back what a recreated exit device took with it (an Xray restart
// remakes every psi- TUN, relay.Up every wgrN), without restarting anything.
func (m *Manager) ReassertRoutes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.procs {
		for _, r := range cur.routes {
			// A country with no exit is masqueraded here; a table would cut it off.
			if strings.TrimSpace(r.Exit) == "" {
				continue
			}
			through, wrote, err := reassertPool(r.poolExit())
			switch {
			case err != nil:
				logger.Debugf("ocserv: could not reassert %s on %s: %v", r.CIDR, r.Exit, err)
			case wrote && through:
				logger.Infof("ocserv: %s leaves through %s again", r.CIDR, r.Exit)
			case wrote:
				logger.Infof("ocserv: %s is held closed until %s is back", r.CIDR, r.Exit)
			}
		}
	}
}

// removeLocationRoute takes a country's pool back off the wire. A rule left
// behind would keep sending an address range into a tunnel nobody serves any
// more, and the next inbound to be handed that range would vanish.
//
// What a wanted country holds now is left to it: two countries trading tables
// would otherwise each flush the other's, and a pool kept under a new table
// would lose its NAT and its exit's rewrite.
func removeLocationRoute(r locationRoute, wanted []locationRoute) {
	if !onLinux || r.CIDR == "" {
		return
	}
	poolKept := slices.ContainsFunc(wanted, func(w locationRoute) bool { return w.CIDR == r.CIDR })
	tableKept := slices.ContainsFunc(wanted, func(w locationRoute) bool { return w.Table == r.Table })
	if !poolKept {
		removeExitSNAT(r.CIDR)
	}
	table := strconv.Itoa(r.Table)
	for ipRuleExists(r.CIDR, r.Table) {
		if err := run("ip", "rule", "del", "from", r.CIDR, "lookup", table); err != nil {
			break
		}
	}
	if !tableKept {
		_ = run("ip", "route", "flush", "table", table)
	}
	if !poolKept {
		removeNAT(r.CIDR)
	}
}

// locationRoutes is what an instance's locations need on the wire.
func (inst Instance) locationRoutes() []locationRoute {
	locs, _ := inst.locations()
	if len(locs) == 0 {
		return nil
	}
	routes := make([]locationRoute, 0, len(locs))
	for i, l := range locs {
		routes = append(routes, locationRoute{
			CIDR:  l.CIDR(inst.Id, i),
			Exit:  strings.TrimSpace(l.Exit),
			Table: routeTableFor(inst.Id, i),
		})
	}
	return routes
}

// syncLocationRoutes brings the wire in line with what the instance wants and
// removes whatever the previous version of it left behind. Removing first
// would briefly cut a country that is only being renumbered, so the wanted
// routes go up before the stale ones come down.
func syncLocationRoutes(previous, wanted []locationRoute) {
	for _, r := range wanted {
		ensureLocationRoute(r)
	}
	keep := make(map[string]struct{}, len(wanted))
	for _, r := range wanted {
		keep[r.CIDR+"|"+strconv.Itoa(r.Table)] = struct{}{}
	}
	for _, r := range previous {
		if _, still := keep[r.CIDR+"|"+strconv.Itoa(r.Table)]; still {
			continue
		}
		removeLocationRoute(r, wanted)
	}
}
