package ocserv

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
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

func runOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), natTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// ipRuleExists reports whether the rule steering this pool is already in
// place, so a reconcile does not stack a second copy of it every time.
func ipRuleExists(cidr string, table int) bool {
	out, err := runOut("ip", "rule", "show")
	if err != nil {
		return false
	}
	want := fmt.Sprintf("from %s lookup %d", cidr, table)
	wantNamed := fmt.Sprintf("from %s lookup ", cidr)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return true
		}
		// A table that also has a name in rt_tables prints the name rather
		// than the number, so the number alone would never match and the
		// rule would be added again on every single reconcile.
		if strings.Contains(line, wantNamed) && strings.Contains(line, "priority "+strconv.Itoa(table)) {
			return true
		}
	}
	return false
}

// ensureLocationRoute puts one country's pool on the right path.
//
// With no exit the pool leaves through this host and is masqueraded here, the
// same as an inbound's own pool. With an exit it must NOT be masqueraded here:
// the rule nat.go writes has no outgoing interface, so it would rewrite the
// packets heading into the tunnel too, and the server at the far end would
// have nothing left to route the answers back to.
func ensureLocationRoute(r locationRoute) {
	if runtime.GOOS != "linux" || r.CIDR == "" {
		return
	}
	exit := strings.TrimSpace(r.Exit)
	if exit == "" {
		removeLocationRoute(locationRoute{CIDR: r.CIDR, Exit: "x", Table: r.Table})
		ensureNAT(r.CIDR)
		return
	}

	removeNAT(r.CIDR)
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		logger.Warningf("ocserv: could not enable ip_forward: %v", err)
	}
	table := strconv.Itoa(r.Table)
	// replace rather than add: an exit the operator has just changed should
	// take effect, not collide with the route that is already in the table.
	if err := run("ip", "route", "replace", "default", "dev", exit, "table", table); err != nil {
		logger.Warningf("ocserv: %s cannot be routed out of %q — the interface is missing or down, and clients who pick that location will reach nothing: %v", r.CIDR, exit, err)
		return
	}
	if !ipRuleExists(r.CIDR, r.Table) {
		if err := run("ip", "rule", "add", "from", r.CIDR, "lookup", table, "priority", table); err != nil {
			logger.Warningf("ocserv: could not steer %s into table %s: %v", r.CIDR, table, err)
			return
		}
	}
	logger.Infof("ocserv: %s now leaves through %s", r.CIDR, exit)
}

// removeLocationRoute takes a country's pool back off the wire. A rule left
// behind would keep sending an address range into a tunnel nobody serves any
// more, and the next inbound to be handed that range would vanish.
func removeLocationRoute(r locationRoute) {
	if runtime.GOOS != "linux" || r.CIDR == "" {
		return
	}
	table := strconv.Itoa(r.Table)
	for ipRuleExists(r.CIDR, r.Table) {
		if err := run("ip", "rule", "del", "from", r.CIDR, "lookup", table); err != nil {
			break
		}
	}
	_ = run("ip", "route", "flush", "table", table)
	removeNAT(r.CIDR)
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
		removeLocationRoute(r)
	}
}
