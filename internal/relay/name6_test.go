package relay

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

// answer is what one family's query for the name gave: its records, or an error.
type answer struct {
	addrs []netip.Addr
	err   error
}

// fakeResolver answers like Go's default resolver, StrictErrors off: "ip4" and
// "ip6" ask one family, "ip" asks both and keeps what answered if one failed.
func fakeResolver(t *testing.T, a, aaaa answer) {
	t.Helper()
	saved := lookupName6
	lookupName6 = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > lookupName6Timeout {
			t.Errorf("looked up %s with deadline %v (set %v); want at most %s", host, deadline, ok, lookupName6Timeout)
		}
		switch network {
		case "ip4":
			return a.addrs, a.err
		case "ip6":
			return aaaa.addrs, aaaa.err
		}
		if a.err != nil && aaaa.err != nil {
			return nil, aaaa.err
		}
		return append(append([]netip.Addr(nil), a.addrs...), aaaa.addrs...), nil
	}
	t.Cleanup(func() { lookupName6 = saved })
}

// fakeZone is a resolver holding these records; a family with none is not found.
// Like Go's own filter, an IPv4 in its 16-byte form is an A record.
func fakeZone(t *testing.T, records ...string) {
	t.Helper()
	notFound := &net.DNSError{Err: "no such host", Name: "v6.example.com", IsNotFound: true}
	a, aaaa := answer{err: notFound}, answer{err: notFound}
	for _, r := range records {
		addr := netip.MustParseAddr(r)
		family := &aaaa
		if addr.Unmap().Is4() {
			family = &a
		}
		family.addrs, family.err = append(family.addrs, addr), nil
	}
	fakeResolver(t, a, aaaa)
}

// The IPv6 name exists so a customer's phone lands on the front's IPv6; an A
// record beside it hands some phones the IPv4 that MCI freezes after 14 KB.
func TestTheIPv6NameIsCheckedAgainstTheAddress(t *testing.T) {
	const (
		address6 = "2a01:4f8:c17:1234::1"
		noAAAA   = "v6.example.com has no AAAA record for 2a01:4f8:c17:1234::1"
		alsoA    = "v6.example.com also has an A record, so some clients will use IPv4"
	)
	for _, tc := range []struct {
		records []string
		want    string
	}{
		{[]string{address6}, ""},
		{[]string{"2a01:4f8:c17:1234::2"}, noAAAA},
		{[]string{address6, "198.51.100.5"}, alsoA},
		// How an /etc/hosts entry comes back: an IPv4 in its 16-byte form.
		{[]string{address6, "::ffff:198.51.100.5"}, alsoA},
		{[]string{"198.51.100.5"}, noAAAA + "; " + alsoA},
		{nil, noAAAA},
	} {
		fakeZone(t, tc.records...)
		if got := Name6Problem(t.Context(), "v6.example.com", address6); got != tc.want {
			t.Errorf("records %v: got %q; want %q", tc.records, got, tc.want)
		}
	}
}

// A resolver that failed has said nothing about the records, so the note says
// it could not look; asked both at once, Go hides a failed family behind the other.
func TestAFailedLookupIsNotAMissingRecord(t *testing.T) {
	const want = "could not look up v6.example.com: server misbehaving"
	servfail := answer{err: &net.DNSError{Err: "server misbehaving", Name: "v6.example.com", IsTemporary: true}}
	aaaa := answer{addrs: []netip.Addr{netip.MustParseAddr("2a01:4f8:c17:1234::1")}}
	a := answer{addrs: []netip.Addr{netip.MustParseAddr("198.51.100.5")}}
	timedOut := answer{err: context.DeadlineExceeded}
	for _, tc := range []struct {
		name    string
		a, aaaa answer
		want    string
	}{
		{"AAAA failed, A answered", a, servfail, want},
		{"A failed, AAAA answered", servfail, aaaa, want},
		{"the clock ran out", timedOut, timedOut, "could not look up v6.example.com: context deadline exceeded"},
	} {
		fakeResolver(t, tc.a, tc.aaaa)
		if got := Name6Problem(t.Context(), "v6.example.com", "2a01:4f8:c17:1234::1"); got != tc.want {
			t.Errorf("%s: got %q; want %q", tc.name, got, tc.want)
		}
	}
}
