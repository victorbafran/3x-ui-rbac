package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const lookupName6Timeout = 5 * time.Second

// lookupName6 is how the panel resolves a front's IPv6 name; a test stands in for it.
var lookupName6 = net.DefaultResolver.LookupNetIP

// Name6Problem says why a phone given name may not land on address6: no AAAA
// record for it, or an A record that hands out IPv4; "" is a name that fits.
func Name6Problem(ctx context.Context, name, address6 string) string {
	ctx, cancel := context.WithTimeout(ctx, lookupName6Timeout)
	defer cancel()
	// One family per query: asked together, Go drops a family whose query failed.
	aaaa, failed := lookupFamily(ctx, "ip6", name)
	if failed != "" {
		return failed
	}
	a, failed := lookupFamily(ctx, "ip4", name)
	if failed != "" {
		return failed
	}
	want, _ := netip.ParseAddr(address6)
	pointsAtIt, hasA := false, false
	for _, addr := range aaaa {
		pointsAtIt = pointsAtIt || addr.Unmap() == want
	}
	for _, addr := range a {
		hasA = hasA || addr.Unmap().Is4()
	}
	var problems []string
	if !pointsAtIt {
		problems = append(problems, fmt.Sprintf("%s has no AAAA record for %s", name, address6))
	}
	if hasA {
		problems = append(problems, fmt.Sprintf("%s also has an A record, so some clients will use IPv4", name))
	}
	return strings.Join(problems, "; ")
}

// lookupFamily is name's records of one family, none when it has no such record,
// or else why the lookup failed.
func lookupFamily(ctx context.Context, network, name string) ([]netip.Addr, string) {
	addrs, err := lookupName6(ctx, network, name)
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		return addrs, ""
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return nil, ""
	case dnsErr != nil:
		return nil, fmt.Sprintf("could not look up %s: %s", name, dnsErr.Err)
	default:
		return nil, fmt.Sprintf("could not look up %s: %v", name, err)
	}
}
