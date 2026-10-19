package ocserv

import (
	"fmt"
	"strconv"
	"strings"
)

// Location is one country an inbound offers. The customer picks it from the
// Group dropdown their AnyConnect client shows before asking for a password;
// the panel gives each one a pool of its own so the host can route that pool
// to wherever that country's traffic should leave.
//
// Measured on ocserv 1.2.4, two groups on one instance: the client that picked
// "germany" was handed 10.91.0.141 with that group's DNS and the one that
// picked "turkey" 10.92.0.141 with the other's, so a location really is the
// unit a route can be hung on.
type Location struct {
	// Name is the group name ocserv matches on and the password file lists
	// per client. It is an identifier rather than a label: the password file
	// is colon-separated and its group field comma-separated, so neither
	// character can appear here.
	Name string

	// Label is what the customer reads in the dropdown. Empty shows Name.
	Label string

	// Network and Netmask are this location's own pool. Empty takes
	// LocationPool, which keeps every country clear of every other and of
	// every inbound's own pool.
	Network string
	Netmask string

	// DNS is this location's resolver list. Empty falls back to the
	// inbound's own, which is usually what a second country wants anyway.
	DNS []string

	// Exit is the interface this country's traffic leaves through — in
	// practice a tunnel to a server standing in that country. Empty means it
	// leaves through this server, which is the right answer for the country
	// the server is already in and costs nothing extra.
	Exit string
}

// locationsPerInbound caps how many countries one inbound may offer. The cap
// exists so pool and routing-table numbers stay a simple function of the
// inbound id: raising it would renumber every location on every other inbound.
const locationsPerInbound = 16

// routeTableBase is where this package's routing tables start. Linux reserves
// 253-255 and most hand-written rules sit in the low hundreds, so starting
// well clear of both keeps the panel out of an operator's own tables.
const routeTableBase = 7000

// LocationPool derives the /24 a location hands out.
//
// DefaultPool walks 10.8.x.0 through 10.247.x.0 for the inbounds themselves,
// so locations take the block above it that nothing else reaches. Deriving
// rather than asking means two countries can never be given the same addresses
// by an operator who was not counting.
func LocationPool(inboundID, index int) string {
	n := inboundID*locationsPerInbound + index
	if n < 0 {
		n = -n
	}
	// 10.248.0.0 upward: 8 second octets × 256 third octets, which is every
	// location of 128 inbounds and still outside DefaultPool's range.
	return fmt.Sprintf("10.%d.%d.0", 248+(n/256)%8, n%256)
}

// routeTableFor is the routing table one location's pool is steered into. Each
// location gets its own so removing one country never disturbs another.
func routeTableFor(inboundID, index int) int {
	n := inboundID*locationsPerInbound + index
	if n < 0 {
		n = -n
	}
	return routeTableBase + n
}

// ValidLocationName reports whether a name can be written to both files that
// have to hold it. ocserv's password file is colon-separated and its group
// field comma-separated, and the per-group config is one file named after the
// group, so a name carrying a separator or a path element would let one
// location rewrite another's.
func ValidLocationName(name string) error {
	if name == "" {
		return fmt.Errorf("location name is empty")
	}
	if strings.ContainsAny(name, ":,/\\\n\r \t") {
		return fmt.Errorf("location name %q contains a character the password file or a filename cannot hold (one of : , / \\ space or a line break)", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("location name %q is not a usable filename", name)
	}
	return nil
}

// labelOrName is what the customer sees. ocserv writes the label inside square
// brackets, so a label carrying one would truncate the dropdown entry.
func (l Location) labelOrName() string {
	label := strings.TrimSpace(l.Label)
	label = strings.NewReplacer("[", "", "]", "", "\n", " ", "\r", " ").Replace(label)
	if label == "" {
		return l.Name
	}
	return label
}

// pool returns the location's network, falling back to the derived one.
func (l Location) pool(inboundID, index int) (network, netmask string) {
	network = strings.TrimSpace(l.Network)
	if network == "" {
		network = LocationPool(inboundID, index)
	}
	netmask = strings.TrimSpace(l.Netmask)
	if netmask == "" {
		netmask = "255.255.255.0"
	}
	return network, netmask
}

// CIDR renders a location's pool as a prefix, for the routing rule.
func (l Location) CIDR(inboundID, index int) string {
	network, netmask := l.pool(inboundID, index)
	return network + "/" + strconv.Itoa(maskBits(netmask))
}

// locations returns the inbound's locations with every name that cannot be
// used dropped, and any duplicate name after the first dropped with it. A
// broken entry takes itself out rather than the whole instance: the other
// countries keep working and the operator sees one country missing.
func (inst Instance) locations() ([]Location, []string) {
	if len(inst.Locations) == 0 {
		return nil, nil
	}
	out := make([]Location, 0, len(inst.Locations))
	var problems []string
	seen := make(map[string]struct{}, len(inst.Locations))
	for _, l := range inst.Locations {
		l.Name = strings.TrimSpace(l.Name)
		if err := ValidLocationName(l.Name); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if _, dup := seen[l.Name]; dup {
			problems = append(problems, fmt.Sprintf("location %q is listed twice; the second is ignored", l.Name))
			continue
		}
		if len(out) >= locationsPerInbound {
			problems = append(problems, fmt.Sprintf("location %q is past the limit of %d per inbound and is ignored", l.Name, locationsPerInbound))
			continue
		}
		seen[l.Name] = struct{}{}
		out = append(out, l)
	}
	return out, problems
}

// renderGroupConfig is the file ocserv reads when a client picks this
// location: the pool it hands out and the resolvers that go with it.
func renderGroupConfig(inst Instance, l Location, index int) string {
	var b strings.Builder
	network, netmask := l.pool(inst.Id, index)
	fmt.Fprintf(&b, "# generated by x-ui for inbound %d, location %s — edits are overwritten\n", inst.Id, l.Name)
	fmt.Fprintf(&b, "ipv4-network = %s\n", network)
	fmt.Fprintf(&b, "ipv4-netmask = %s\n", netmask)
	dns := l.DNS
	if len(dns) == 0 {
		dns = inst.dns()
	}
	for _, d := range dns {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		fmt.Fprintf(&b, "dns = %s\n", d)
	}
	return b.String()
}
