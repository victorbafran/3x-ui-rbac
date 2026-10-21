package tunnel

import "sort"

// Which ports a front machine opens for customers.
//
// This is the thing that was done by hand on the first real tunnel, and the
// hand is why an inbound created afterwards silently did not work: the front
// forwarded 443, 1194 and 41330, the operator added an inbound on 10434, and
// nothing on the front was listening for it. The panel knew the port the
// whole time and nobody asked it.

// Inbound is the little the port list needs to know about one inbound.
type Inbound struct {
	Port    int
	Enabled bool
	// Local is false for an inbound served by a node rather than by this
	// panel. A front in front of this panel cannot carry it: the traffic
	// would arrive here and have nowhere to go.
	Local bool
}

// Reserved is what must never be forwarded, whatever the inbounds say.
type Reserved struct {
	// PanelPort is the panel's own web port. Forwarding it would publish the
	// admin login on the front machine's address, which is a different and
	// much worse thing than publishing a VPN port.
	PanelPort int
	// TunnelPort is the port the panel dials in on. Forwarding it would have
	// the front send the panel's own dial-in back down the tunnel.
	TunnelPort int
	// SSHPort keeps an operator from locking themselves out of the front by
	// forwarding its own management port into the tunnel.
	SSHPort int
}

// ForwardedPorts is the list a front should open, derived rather than typed.
//
// Sorted and deduplicated so a reconcile that produces the same set in a
// different order does not look like a change and restart the tunnel, which
// would drop every customer riding it.
func ForwardedPorts(inbounds []Inbound, reserved Reserved) []int {
	blocked := map[int]bool{}
	for _, p := range []int{reserved.PanelPort, reserved.TunnelPort, reserved.SSHPort} {
		if p > 0 {
			blocked[p] = true
		}
	}

	seen := map[int]bool{}
	out := make([]int, 0, len(inbounds))
	for _, ib := range inbounds {
		if !ib.Enabled || !ib.Local {
			continue
		}
		if ib.Port <= 0 || ib.Port > 65535 || blocked[ib.Port] || seen[ib.Port] {
			continue
		}
		seen[ib.Port] = true
		out = append(out, ib.Port)
	}
	sort.Ints(out)
	return out
}

// SamePorts reports whether two lists are the same set, so a reconcile can
// tell "nothing to do" from "rewrite the front and restart it".
//
// Both sides come from ForwardedPorts and are therefore sorted; comparing
// element by element is enough and does not allocate.
func SamePorts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
