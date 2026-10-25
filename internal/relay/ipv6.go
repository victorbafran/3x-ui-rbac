package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A front's IPv6 is handed to customers because MCI freezes TCP to an unknown
// IPv4 after 14 KB and leaves IPv6 open. These read, check and reach it.

// What a front's customers are given: IPv4 as before, IPv6 instead, or both.
const (
	IPFamilyV4   = ""
	IPFamilyV6   = "v6"
	IPFamilyBoth = "both"
)

var (
	errNotIPv6       = errors.New("is not an IPv6 address")
	errNotPublicIPv6 = errors.New("is not a public IPv6 address")
	// ErrNoPublicIPv6 is a server that answered and holds no address to hand out.
	ErrNoPublicIPv6 = errors.New("the server has no public IPv6 address")
)

// ParseIPFamily refuses anything but the three families, so a typo is not
// stored as a fourth that every output would read as IPv4.
func ParseIPFamily(s string) (string, error) {
	switch family := strings.TrimSpace(s); family {
	case IPFamilyV4, IPFamilyV6, IPFamilyBoth:
		return family, nil
	}
	return "", fmt.Errorf("customers connect over IPv4 (\"\"), IPv6 (%q) or both (%q), not %q", IPFamilyV6, IPFamilyBoth, s)
}

var (
	// Every global unicast block IANA hands out lies in 2000::/3; Go's
	// IsGlobalUnicast also passes ::a.b.c.d, site-local and NAT64 addresses.
	globalUnicast6 = netip.MustParsePrefix("2000::/3")
	// 6to4 and Teredo carry IPv6 inside IPv4, the very path MCI freezes.
	ipv6OverIPv4 = []netip.Prefix{netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32")}
)

// NormalizeAddress6 accepts a public IPv6, bracketed or bare, and returns it
// bare and canonical; IPv4 and anything outside native global unicast is refused.
func NormalizeAddress6(s string) (string, error) {
	host := strings.TrimSpace(s)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return "", fmt.Errorf("%q %w", s, errNotIPv6)
	}
	addr, _ := netip.AddrFromSlice(ip)
	if !globalUnicast6.Contains(addr) || slices.ContainsFunc(ipv6OverIPv4, func(p netip.Prefix) bool { return p.Contains(addr) }) {
		return "", fmt.Errorf("%q %w", s, errNotPublicIPv6)
	}
	return ip.String(), nil
}

// The route lookup sends nothing; it only asks the kernel which source it
// would use towards a public address.
const (
	ipv6RouteCommand = "ip -6 route get 2001:4860:4860::8888"
	ipv6AddrCommand  = "ip -6 -o addr show scope global"
	// The main table's IPv4 default is the interface customers already reach;
	// a VPN such as WARP routes from a table of its own and takes nothing in.
	ipv4DefaultCommand = "ip -4 route show default"
)

// commandRunner is the part of an SSH session discovery uses.
type commandRunner interface {
	Run(ctx context.Context, command string) (string, error)
}

// DiscoverIPv6 reads a server's public IPv6 over SSH.
func DiscoverIPv6(ctx context.Context, creds Credentials) (string, error) {
	client, err := Dial(ctx, creds)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return PublicIPv6(ctx, client)
}

// PublicIPv6 asks one session for the server's public IPv6. A failed route
// lookup is a host without an IPv6 default route; the address list still answers.
func PublicIPv6(ctx context.Context, c commandRunner) (string, error) {
	route, _ := c.Run(ctx, ipv6RouteCommand)
	addrs, err := c.Run(ctx, ipv6AddrCommand)
	v4Default, _ := c.Run(ctx, ipv4DefaultCommand)
	onDev := fieldAfter(v4Default, "dev")
	found, elsewhere := pickPublicIPv6(route, addrs, onDev)
	if found != "" {
		return found, nil
	}
	if err != nil {
		return "", fmt.Errorf("could not list the server's IPv6 addresses: %w", err)
	}
	if elsewhere != "" {
		return "", fmt.Errorf("%w on %s, the interface its IPv4 uses; the one on %s is likely a VPN's, such as WARP, which takes no connections in, so type it only if customers can reach it",
			ErrNoPublicIPv6, onDev, elsewhere)
	}
	return "", ErrNoPublicIPv6
}

// pickPublicIPv6 prefers the route's source unless the list marks it as one the
// kernel drops, then the first kept address; on onDev only, when it is known.
func pickPublicIPv6(route, addrs, onDev string) (found, elsewhere string) {
	stable, unusable := globalAddresses(addrs)
	onTheFront := func(dev string) bool { return onDev == "" || dev == onDev }
	if src := routeSource(route); src.ip != "" && !unusable[src.ip] {
		if onTheFront(src.dev) {
			return src.ip, ""
		}
		elsewhere = src.dev
	}
	for _, a := range stable {
		if onTheFront(a.dev) {
			return a.ip, ""
		}
		if elsewhere == "" {
			elsewhere = a.dev
		}
	}
	return "", elsewhere
}

// ifaceAddr is an IPv6 address and the interface the kernel holds it on.
type ifaceAddr struct{ ip, dev string }

func routeSource(out string) ifaceAddr {
	src, err := NormalizeAddress6(fieldAfter(out, "src"))
	if err != nil {
		return ifaceAddr{}
	}
	return ifaceAddr{ip: src, dev: fieldAfter(out, "dev")}
}

// fieldAfter is the word after the first key in out, the way ip prints its pairs.
func fieldAfter(out, key string) string {
	fields := strings.Fields(out)
	i := slices.Index(fields, key)
	if i < 0 || i+1 >= len(fields) {
		return ""
	}
	return fields[i+1]
}

// unusableAddrFlags mark an address that expires within days or that the
// kernel has not, or will not, put into use.
var unusableAddrFlags = []string{"temporary", "deprecated", "tentative", "dadfailed"}

func globalAddresses(out string) (stable []ifaceAddr, unusable map[string]bool) {
	unusable = map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		i := slices.Index(fields, "inet6")
		if i < 1 || i+1 >= len(fields) {
			continue
		}
		bare, _, _ := strings.Cut(fields[i+1], "/")
		addr, err := NormalizeAddress6(bare)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(fields[i+2:], func(f string) bool { return slices.Contains(unusableAddrFlags, f) }) {
			unusable[addr] = true
			continue
		}
		// A container's interface is listed as eth0@if7; its routes say eth0.
		dev, _, _ := strings.Cut(fields[i-1], "@")
		stable = append(stable, ifaceAddr{ip: addr, dev: dev})
	}
	return stable, unusable
}

const reachIPv6Timeout = 5 * time.Second

// dialIPv6 is how the panel reaches a front's IPv6; a test stands in for it.
var dialIPv6 = func(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp6", address)
}

// ReachIPv6 dials one customer port on a front's IPv6 from the panel and says
// why it could not; "" is a front that answered.
func ReachIPv6(ctx context.Context, address6 string, port int) string {
	if port <= 0 {
		return "the front forwards no customer port to try"
	}
	ctx, cancel := context.WithTimeout(ctx, reachIPv6Timeout)
	defer cancel()
	target := net.JoinHostPort(address6, strconv.Itoa(port))
	conn, err := dialIPv6(ctx, target)
	if err == nil {
		_ = conn.Close()
		return ""
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return fmt.Sprintf("no answer from %s within %s", target, reachIPv6Timeout)
	case errors.Is(err, syscall.ENETUNREACH):
		return fmt.Sprintf("this panel has no IPv6 route to %s, so it could not check it", target)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		err = opErr.Err
	}
	return fmt.Sprintf("%s: %v", target, err)
}
