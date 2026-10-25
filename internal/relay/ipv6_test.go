package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A typed address is what customers' configs carry, so it is kept in one
// spelling: brackets dropped, zeros folded, lower case.
func TestATypedIPv6IsStoredBareAndCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"2001:DB8:0:0::1":                  "2001:db8::1",
		"[2001:db8::1]":                    "2001:db8::1",
		" 2a01:4f8:c17:1234:0:0:0:1 ":      "2a01:4f8:c17:1234::1",
		"[2606:4700:0000:0000:0000::6815]": "2606:4700::6815",
	} {
		got, err := NormalizeAddress6(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddress6(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// Only an address a phone on MCI can dial is worth handing out; the rest would
// reach the customer as a config that never connects.
func TestOnlyAPublicIPv6IsAccepted(t *testing.T) {
	for in, want := range map[string]error{
		"203.0.113.9":         errNotIPv6,
		"::ffff:203.0.113.9":  errNotIPv6,
		"tun.example.com":     errNotIPv6,
		"[2001:db8::1]:443":   errNotIPv6,
		"[2001:db8::1":        errNotIPv6,
		"fe80::1%eth0":        errNotIPv6,
		"":                    errNotIPv6,
		"::1":                 errNotPublicIPv6,
		"fe80::1":             errNotPublicIPv6,
		"fd00::1":             errNotPublicIPv6,
		"fc12:3456::9":        errNotPublicIPv6,
		"ff02::1":             errNotPublicIPv6,
		"::":                  errNotPublicIPv6,
		"[fd12:3456:789a::1]": errNotPublicIPv6,
		// Global to Go, yet outside 2000::/3 or wrapped in IPv4: none reaches a front.
		"::203.0.113.9":                        errNotPublicIPv6,
		"fec0::1":                              errNotPublicIPv6,
		"64:ff9b::203.0.113.9":                 errNotPublicIPv6,
		"100::1":                               errNotPublicIPv6,
		"2002:cb00:7109::1":                    errNotPublicIPv6,
		"2001:0:4136:e378:8000:63bf:3fff:fdd2": errNotPublicIPv6,
	} {
		got, err := NormalizeAddress6(in)
		if !errors.Is(err, want) {
			t.Errorf("NormalizeAddress6(%q) = %q, %v; want %v", in, got, err, want)
		}
	}
}

func TestTheFamilyIsOneOfThree(t *testing.T) {
	for in, want := range map[string]string{"": IPFamilyV4, "v6": IPFamilyV6, "both": IPFamilyBoth, " both ": IPFamilyBoth} {
		if got, err := ParseIPFamily(in); err != nil || got != want {
			t.Errorf("ParseIPFamily(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"v4", "V6", "ipv6", "dual", "4"} {
		if got, err := ParseIPFamily(bad); err == nil {
			t.Errorf("ParseIPFamily(%q) = %q; a fourth family was accepted", bad, got)
		}
	}
}

const (
	stableAddr    = "2: eth0    inet6 2a01:4f8:c17:1234::1/64 scope global \\       valid_lft forever preferred_lft forever\n"
	temporaryAddr = "2: eth0    inet6 2a01:4f8:c17:1234:5d1e:9a3b:7c2f:11aa/64 scope global temporary dynamic \\       valid_lft 604700sec preferred_lft 85900sec\n"
	routeViaEth0  = "2001:4860:4860::8888 from :: via fe80::1 dev eth0 proto ra src %s metric 1024 hoplimit 64 pref medium\n"
)

// The kernel's own source address towards the internet is the one replies
// come back to, so it wins over whatever the address list happens to put first.
func TestTheRouteSourceIsPreferred(t *testing.T) {
	route := fmt.Sprintf(routeViaEth0, "2a01:4f8:c17:1234::99")
	addrs := stableAddr + "2: eth0    inet6 2a01:4f8:c17:1234::99/64 scope global \\       valid_lft forever preferred_lft forever\n"
	if got, _ := pickPublicIPv6(route, addrs, "eth0"); got != "2a01:4f8:c17:1234::99" {
		t.Fatalf("picked %q; want the route's source 2a01:4f8:c17:1234::99", got)
	}
}

const (
	warpAddr     = "4: wgcf    inet6 2606:4700:110:8a36:df92:102a:9602:fa18/128 scope global \\       valid_lft forever preferred_lft forever\n"
	routeViaWarp = "2001:4860:4860::8888 from :: dev wgcf proto kernel src 2606:4700:110:8a36:df92:102a:9602:fa18 metric 1024 pref medium\n"
	v4ViaEth0    = "default via 172.31.1.1 dev eth0 proto dhcp src 203.0.113.9 metric 100\n"
)

// WARP's policy routing makes its tunnel the IPv6 route, but its address takes
// no connection in; only an address beside the server's own IPv4 is handed out.
func TestAVPNsIPv6IsNotTheFronts(t *testing.T) {
	native := "2: eth0@if7    inet6 2a01:4f8:c17:1234::1/64 scope global \\       valid_lft forever preferred_lft forever\n"
	shell := func(addrs string) fakeShell {
		return fakeShell{
			ipv6RouteCommand:   {out: routeViaWarp},
			ipv6AddrCommand:    {out: addrs},
			ipv4DefaultCommand: {out: v4ViaEth0},
		}
	}
	got, err := PublicIPv6(t.Context(), shell(warpAddr+native))
	if err != nil || got != "2a01:4f8:c17:1234::1" {
		t.Errorf("WARP beside a native IPv6: got %q, %v; want eth0's 2a01:4f8:c17:1234::1", got, err)
	}
	got, err = PublicIPv6(t.Context(), shell(warpAddr))
	if !errors.Is(err, ErrNoPublicIPv6) || !strings.Contains(err.Error(), "wgcf") {
		t.Errorf("WARP's IPv6 alone: got %q, %v; want ErrNoPublicIPv6 naming wgcf", got, err)
	}
}

// A privacy address expires within days and a NAT66 source is nobody's to
// dial; either would hand customers a config that dies on its own.
func TestARouteSourceThatWillNotLastFallsBackToAStableAddress(t *testing.T) {
	for name, route := range map[string]string{
		"temporary": fmt.Sprintf(routeViaEth0, "2a01:4f8:c17:1234:5d1e:9a3b:7c2f:11aa"),
		"ula":       fmt.Sprintf(routeViaEth0, "fd00:dead:beef::2"),
		"none":      "",
	} {
		if got, _ := pickPublicIPv6(route, temporaryAddr+stableAddr, ""); got != "2a01:4f8:c17:1234::1" {
			t.Errorf("%s route source: picked %q; want the stable 2a01:4f8:c17:1234::1", name, got)
		}
	}
}

func TestTheAddressListSkipsWhatTheKernelWillNotKeep(t *testing.T) {
	addrs := "5: sit0    inet6 ::203.0.113.9/96 scope global \\       valid_lft forever preferred_lft forever\n" +
		temporaryAddr +
		"2: eth0    inet6 2a01:4f8:c17:1234::5/64 scope global deprecated dynamic mngtmpaddr \\       valid_lft 3000sec preferred_lft 0sec\n" +
		"2: eth0    inet6 2a01:4f8:c17:1234::6/128 scope global tentative \\       valid_lft forever preferred_lft forever\n" +
		"2: eth0    inet6 2a01:4f8:c17:1234::7/128 scope global dadfailed tentative \\       valid_lft forever preferred_lft forever\n" +
		"3: wg0    inet6 fd42:42:42::1/64 scope global \\       valid_lft forever preferred_lft forever\n" +
		"2: eth0    inet6 2a01:4f8:c17:1234::8/64 scope global dynamic mngtmpaddr \\       valid_lft 86000sec preferred_lft 14000sec\n"
	if got, _ := pickPublicIPv6("", addrs, ""); got != "2a01:4f8:c17:1234::8" {
		t.Fatalf("picked %q; want 2a01:4f8:c17:1234::8, the first one the kernel keeps", got)
	}
	if got, _ := pickPublicIPv6("", temporaryAddr, ""); got != "" {
		t.Fatalf("picked %q from a list holding only a privacy address", got)
	}
}

type fakeShell map[string]struct {
	out string
	err error
}

func (f fakeShell) Run(_ context.Context, command string) (string, error) {
	if r, ok := f[command]; ok {
		return r.out, r.err
	}
	return "", fmt.Errorf("unexpected command %q", command)
}

func TestTheServersIPv6IsReadOverSSH(t *testing.T) {
	noRoute := errors.New("Process exited with status 2: RTNETLINK answers: Network is unreachable")
	got, err := PublicIPv6(t.Context(), fakeShell{
		ipv6RouteCommand: {err: noRoute},
		ipv6AddrCommand:  {out: stableAddr},
	})
	if err != nil || got != "2a01:4f8:c17:1234::1" {
		t.Errorf("no IPv6 route but a stable address: got %q, %v", got, err)
	}

	got, err = PublicIPv6(t.Context(), fakeShell{ipv6RouteCommand: {err: noRoute}, ipv6AddrCommand: {}})
	if !errors.Is(err, ErrNoPublicIPv6) {
		t.Errorf("a server with no IPv6: got %q, %v; want ErrNoPublicIPv6", got, err)
	}

	listFailed := errors.New("bash: ip: command not found")
	got, err = PublicIPv6(t.Context(), fakeShell{ipv6RouteCommand: {err: listFailed}, ipv6AddrCommand: {err: listFailed}})
	if !errors.Is(err, listFailed) {
		t.Errorf("an unreadable server: got %q, %v; want the reason it could not be read", got, err)
	}
}

func fakeIPv6Dial(t *testing.T, dial func(ctx context.Context, address string) (net.Conn, error)) {
	t.Helper()
	saved := dialIPv6
	dialIPv6 = dial
	t.Cleanup(func() { dialIPv6 = saved })
}

// Without its own deadline the dial waits out the kernel's two minutes, and
// the front's provisioning with it.
func TestTheReachCheckDialsTheBracketedAddressWithinFiveSeconds(t *testing.T) {
	var dialled string
	fakeIPv6Dial(t, func(ctx context.Context, address string) (net.Conn, error) {
		dialled = address
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > reachIPv6Timeout {
			t.Errorf("dialled with deadline %v (set %v); want at most %s", deadline, ok, reachIPv6Timeout)
		}
		here, there := net.Pipe()
		_ = there.Close()
		return here, nil
	})
	if problem := ReachIPv6(t.Context(), "2001:db8::1", 443); problem != "" {
		t.Errorf("a front that answered was reported as %q", problem)
	}
	if dialled != "[2001:db8::1]:443" {
		t.Errorf("dialled %q; want [2001:db8::1]:443", dialled)
	}
}

func TestAnUnreachedIPv6SaysWhy(t *testing.T) {
	opErr := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp6", Err: err}
	}
	for want, err := range map[string]error{
		"no answer from [2001:db8::1]:443 within 5s":                                  context.DeadlineExceeded,
		"this panel has no IPv6 route to [2001:db8::1]:443, so it could not check it": opErr(os.NewSyscallError("connect", syscall.ENETUNREACH)),
		"[2001:db8::1]:443: connect: connection refused":                              opErr(errors.New("connect: connection refused")),
	} {
		fakeIPv6Dial(t, func(context.Context, string) (net.Conn, error) { return nil, err })
		if got := ReachIPv6(t.Context(), "2001:db8::1", 443); got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	}
}

func TestNoPortIsNotADial(t *testing.T) {
	fakeIPv6Dial(t, func(context.Context, string) (net.Conn, error) {
		t.Error("dialled with no port to try")
		return nil, errors.New("unreachable")
	})
	if got := ReachIPv6(t.Context(), "2001:db8::1", 0); got != "the front forwards no customer port to try" {
		t.Errorf("got %q", got)
	}
}
