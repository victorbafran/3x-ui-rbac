package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

func frontRequest(address6, family string) RelayRequest {
	return RelayRequest{Name: "us", Address: "198.51.100.5", Role: "front", Address6: address6, IPFamily: family}
}

// The address reaches every customer's config, so it is held to what a phone
// can dial and kept in one spelling.
func TestAFrontsIPv6IsCheckedAndStoredBare(t *testing.T) {
	s := &RelayService{}
	r, err := s.rowFrom(frontRequest(" [2001:DB8:0::1] ", "both"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Address6 != "2001:db8::1" || r.IPFamily != relay.IPFamilyBoth {
		t.Errorf("stored address6 %q family %q; want 2001:db8::1 and both", r.Address6, r.IPFamily)
	}
	for _, bad := range []string{"198.51.100.5", "fd00::1", "fe80::1", "::1", "ff02::1", "[2001:db8::1]:443"} {
		if _, err := s.rowFrom(frontRequest(bad, ""), nil); err == nil || !strings.Contains(err.Error(), "IPv6 address") {
			t.Errorf("address6 %q got %v; want it refused as no public IPv6 address", bad, err)
		}
	}
	if _, err := s.rowFrom(frontRequest("", "dual"), nil); err == nil || !strings.Contains(err.Error(), `not "dual"`) {
		t.Errorf("family dual got %v; want it refused", err)
	}
}

// The IPv6 name lands in every IPv6 config, so it is held to what a phone can
// resolve; an address typed here would hide that the field wants a name.
func TestAFrontsIPv6NameIsANameInOneSpelling(t *testing.T) {
	s := &RelayService{}
	req := frontRequest("2001:db8::1", "both")
	req.PublicName6 = " V6.Example.COM. "
	r, err := s.rowFrom(req, nil)
	if err != nil || r.PublicName6 != "v6.example.com" {
		t.Errorf("stored the IPv6 name %q with error %v; want v6.example.com", r.PublicName6, err)
	}
	// Each refusal says where the typed value does belong: the IPv6 field takes no IPv4.
	const (
		isIPv6 = "is an address; the address goes in the IPv6 field, and here a name whose only record is AAAA to it"
		isIPv4 = "is an IPv4 address; the front's IPv4 goes in its address or public name, and here a name whose only record is AAAA to its IPv6"
	)
	for bad, why := range map[string]string{
		"2001:db8::1":            `the IPv6 name "2001:db8::1" ` + isIPv6,
		"[2001:db8::1]":          `the IPv6 name "[2001:db8::1]" ` + isIPv6,
		"198.51.100.5":           `the IPv6 name "198.51.100.5" ` + isIPv4,
		"https://v6.example.com": "is not a host name",
		"v6.example.com:443":     "is not a host name",
	} {
		req.PublicName6 = bad
		if _, err := s.rowFrom(req, nil); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("IPv6 name %q got %v; want it refused as %q", bad, err, why)
		}
	}
}

// An exit carries no customer, so the fields are dropped rather than refused.
func TestAnExitIgnoresTheIPv6Fields(t *testing.T) {
	r, err := (&RelayService{}).rowFrom(RelayRequest{
		Name: "nl", Address: "198.51.100.1", Address6: "fd00::1", PublicName6: "2001:db8::1", IPFamily: "dual",
	}, nil)
	if err != nil {
		t.Fatalf("an exit was refused over fields it does not have: %v", err)
	}
	if r.Address6 != "" || r.PublicName6 != "" || r.IPFamily != "" || r.Address6Error != "" {
		t.Errorf("an exit stored address6 %q name %q family %q error %q", r.Address6, r.PublicName6, r.IPFamily, r.Address6Error)
	}
	for _, col := range []string{"address6", "public_name6", "ip_family", "address6_error"} {
		if _, ok := editableFields(r)[col]; ok {
			t.Errorf("an edit of an exit writes %s", col)
		}
	}
}

// An edit once dropped public_name; these are written, and the note follows
// the address and the IPv6 name it was about.
func TestAnEditWritesTheFrontsIPv6Choice(t *testing.T) {
	s := &RelayService{}
	req := frontRequest("2001:db8::1", "")
	req.PublicName6 = "v6.example.com"
	base, err := s.rowFrom(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if base.Address6Error != address6NotChecked {
		t.Errorf("a typed address that nothing has dialled carries %q; want %q", base.Address6Error, address6NotChecked)
	}
	// A verdict the panel reached: an edit that keeps the address must keep it too.
	const lastVerdict = "v6.example.com also has an A record, so some clients will use IPv4"
	base.Address6Error = lastVerdict

	for name, tc := range map[string]struct {
		address6, name6, family, wantErr string
	}{
		"same address and name, new family": {"2001:db8::1", "v6.example.com", "v6", lastVerdict},
		"same address, a new name":          {"2001:db8::1", "ipv6.example.com", "v6", address6NotChecked},
		"same address, the name cleared":    {"2001:db8::1", "", "v6", address6NotChecked},
		"a new address":                     {"2001:db8::2", "v6.example.com", "both", address6NotChecked},
		"cleared":                           {"", "", "", ""},
	} {
		req := frontRequest(tc.address6, tc.family)
		req.PublicName6 = tc.name6
		edited, err := s.rowFrom(req, base)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f := editableFields(edited)
		if f["address6"] != tc.address6 || f["public_name6"] != tc.name6 || f["ip_family"] != tc.family || f["address6_error"] != tc.wantErr {
			t.Errorf("%s: the edit writes address6 %v name %v family %v error %v; want %q %q %q %q", name,
				f["address6"], f["public_name6"], f["ip_family"], f["address6_error"], tc.address6, tc.name6, tc.family, tc.wantErr)
		}
	}
}

// fakeFrontIPv6 also refuses a name lookup: a front with no IPv6 name has
// nothing to resolve, and fakeFrontName6 stands in when it has one.
func fakeFrontIPv6(t *testing.T, discover func(context.Context, relay.Credentials) (string, error), reach func(context.Context, string, int) string) {
	t.Helper()
	savedDiscover, savedReach, savedName6 := discoverFrontIPv6, reachFrontIPv6, checkFrontName6
	discoverFrontIPv6, reachFrontIPv6 = discover, reach
	checkFrontName6 = func(_ context.Context, name, _ string) string {
		t.Errorf("looked up the IPv6 name %q", name)
		return ""
	}
	t.Cleanup(func() { discoverFrontIPv6, reachFrontIPv6, checkFrontName6 = savedDiscover, savedReach, savedName6 })
}

func fakeFrontName6(t *testing.T, check func(ctx context.Context, name, address6 string) string) {
	t.Helper()
	saved := checkFrontName6
	checkFrontName6 = check
	t.Cleanup(func() { checkFrontName6 = saved })
}

func neverDiscover(t *testing.T) func(context.Context, relay.Credentials) (string, error) {
	return func(context.Context, relay.Credentials) (string, error) {
		t.Error("the server was asked for an IPv6")
		return "2a01:4f8:c17:1234::99", nil
	}
}

func neverDial(t *testing.T) func(context.Context, string, int) string {
	return func(_ context.Context, address6 string, _ int) string {
		t.Errorf("dialled %s", address6)
		return ""
	}
}

// seedIPv6Front is a ready front carrying one VLESS inbound on 443.
func seedIPv6Front(t *testing.T, role model.RelayRole, address6, address6Error string) model.Relay {
	t.Helper()
	setupRelayDB(t)
	key := filepath.Join(t.TempDir(), "front.key")
	if err := os.WriteFile(key, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	ib := &model.Inbound{Tag: "vless", Enable: true, Port: 443, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	seedRelays(t, model.Relay{
		Name: "us", Address: "198.51.100.5", SSHPort: 22, KeyPath: key, Tunnel: "backpack", Transport: "tcp",
		TunnelPort: 3080, Role: role, Enabled: true, Status: RelayReady, Address6: address6, Address6Error: address6Error,
	})
	return readRelay(t, "us")
}

func readRelay(t *testing.T, name string) model.Relay {
	t.Helper()
	var r model.Relay
	if err := database.GetDB().Where("name = ?", name).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

// A customer port is dialled, not port 80 nor the tunnel port, which binds
// IPv4 only and would call every front's IPv6 dead.
func TestAFrontsIPv6IsReadOffItAndDialled(t *testing.T) {
	front := seedIPv6Front(t, model.RelayFront, "", "")
	var dialled string
	fakeFrontIPv6(t,
		func(_ context.Context, c relay.Credentials) (string, error) {
			if c.Address != "198.51.100.5" || c.Port != 22 {
				t.Errorf("asked %s:%d for the IPv6; want the front's SSH", c.Address, c.Port)
			}
			return "2a01:4f8:c17:1234::1", nil
		},
		func(_ context.Context, address6 string, port int) string {
			dialled = fmt.Sprintf("[%s]:%d", address6, port)
			return ""
		})

	(&RelayService{}).refreshFrontIPv6(front)
	got := readRelay(t, "us")
	if got.Address6 != "2a01:4f8:c17:1234::1" || got.Address6Error != "" {
		t.Errorf("stored address6 %q error %q; want 2a01:4f8:c17:1234::1 reached", got.Address6, got.Address6Error)
	}
	if dialled != "[2a01:4f8:c17:1234::1]:443" {
		t.Errorf("dialled %q; want the customer port [2a01:4f8:c17:1234::1]:443", dialled)
	}
}

// A typed address is the operator's, and an IPv6 the panel cannot reach is a
// note on the row, never a broken front.
func TestATypedIPv6IsDialledNotReplaced(t *testing.T) {
	seedIPv6Front(t, model.RelayFront, "2001:db8::1", address6NotChecked)
	const problem = "no answer from [2001:db8::1]:443 within 5s"
	fakeFrontIPv6(t, neverDiscover(t), func(context.Context, string, int) string { return problem })

	(&RelayService{}).refreshFrontIPv6(readRelay(t, "us"))
	got := readRelay(t, "us")
	if got.Address6 != "2001:db8::1" || got.Address6Error != problem {
		t.Errorf("stored address6 %q error %q; want 2001:db8::1 with %q", got.Address6, got.Address6Error, problem)
	}
	if got.Status != RelayReady || got.LastError != "" {
		t.Errorf("an unreachable IPv6 turned the front %q with %q", got.Status, got.LastError)
	}
}

func TestAFrontWithNoIPv6SaysSo(t *testing.T) {
	seedIPv6Front(t, model.RelayFront, "", "")
	fakeFrontIPv6(t,
		func(context.Context, relay.Credentials) (string, error) { return "", relay.ErrNoPublicIPv6 },
		neverDial(t))

	(&RelayService{}).refreshFrontIPv6(readRelay(t, "us"))
	got := readRelay(t, "us")
	if got.Address6 != "" || got.Address6Error != relay.ErrNoPublicIPv6.Error() || got.Status != RelayReady {
		t.Errorf("stored address6 %q error %q status %q; want none, %q, ready",
			got.Address6, got.Address6Error, got.Status, relay.ErrNoPublicIPv6)
	}
}

// The check runs behind the operator's save, so an address typed while it
// runs must survive both the one read off the server and its dial's verdict.
func TestAnAddressTypedDuringTheCheckIsKept(t *testing.T) {
	typed := func(t *testing.T) {
		t.Helper()
		if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "us").
			Updates(map[string]any{"address6": "2001:db8::77", "address6_error": address6NotChecked}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Run("during discovery", func(t *testing.T) {
		front := seedIPv6Front(t, model.RelayFront, "", "")
		fakeFrontIPv6(t,
			func(context.Context, relay.Credentials) (string, error) { typed(t); return "2a01:4f8:c17:1234::1", nil },
			neverDial(t))
		(&RelayService{}).refreshFrontIPv6(front)
		if got := readRelay(t, "us"); got.Address6 != "2001:db8::77" || got.Address6Error != address6NotChecked {
			t.Errorf("stored address6 %q error %q; want the typed 2001:db8::77 unchecked", got.Address6, got.Address6Error)
		}
	})
	t.Run("during the dial", func(t *testing.T) {
		front := seedIPv6Front(t, model.RelayFront, "2001:db8::1", address6NotChecked)
		fakeFrontIPv6(t, neverDiscover(t),
			func(context.Context, string, int) string {
				typed(t)
				return "no answer from [2001:db8::1]:443 within 5s"
			})
		(&RelayService{}).refreshFrontIPv6(front)
		if got := readRelay(t, "us"); got.Address6 != "2001:db8::77" || got.Address6Error != address6NotChecked {
			t.Errorf("stored address6 %q error %q; want the typed 2001:db8::77 unchecked", got.Address6, got.Address6Error)
		}
	})
}

func nameTheFront(t *testing.T, name6 string) model.Relay {
	t.Helper()
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "us").Update("public_name6", name6).Error; err != nil {
		t.Fatal(err)
	}
	return readRelay(t, "us")
}

const name6AlsoA = "v6.example.com also has an A record, so some clients will use IPv4"

// A name with an A record, or no AAAA for the address, sends phones elsewhere;
// the row says so beside the dial's verdict and the front stays ready.
func TestTheIPv6NameIsResolvedAgainstTheAddressItServes(t *testing.T) {
	t.Run("a typed address", func(t *testing.T) {
		seedIPv6Front(t, model.RelayFront, "2001:db8::1", address6NotChecked)
		front := nameTheFront(t, "v6.example.com")
		const noAnswer = "no answer from [2001:db8::1]:443 within 5s"
		fakeFrontIPv6(t, neverDiscover(t), func(context.Context, string, int) string { return noAnswer })
		var asked string
		fakeFrontName6(t, func(_ context.Context, name, address6 string) string {
			asked = name + " for " + address6
			return name6AlsoA
		})
		(&RelayService{}).refreshFrontIPv6(front)
		got := readRelay(t, "us")
		if asked != "v6.example.com for 2001:db8::1" {
			t.Errorf("checked %q; want v6.example.com for 2001:db8::1", asked)
		}
		if got.Address6Error != noAnswer+"; "+name6AlsoA || got.Status != RelayReady || got.LastError != "" {
			t.Errorf("stored error %q status %q last error %q; want both notes on a ready front", got.Address6Error, got.Status, got.LastError)
		}
	})
	t.Run("an address read off the server", func(t *testing.T) {
		seedIPv6Front(t, model.RelayFront, "", "")
		front := nameTheFront(t, "v6.example.com")
		fakeFrontIPv6(t,
			func(context.Context, relay.Credentials) (string, error) { return "2a01:4f8:c17:1234::1", nil },
			func(context.Context, string, int) string { return "" })
		var asked string
		fakeFrontName6(t, func(_ context.Context, _, address6 string) string { asked = address6; return "" })
		(&RelayService{}).refreshFrontIPv6(front)
		if asked != "2a01:4f8:c17:1234::1" {
			t.Errorf("the name was checked against %q; want the address just read, 2a01:4f8:c17:1234::1", asked)
		}
		if got := readRelay(t, "us"); got.Address6Error != "" {
			t.Errorf("a reached address with a fitting name stored %q", got.Address6Error)
		}
	})
}

// A name typed while the check runs is the operator's, so the verdict about
// the one it replaced is dropped.
func TestANameTypedDuringTheCheckIsKept(t *testing.T) {
	seedIPv6Front(t, model.RelayFront, "2001:db8::1", address6NotChecked)
	front := nameTheFront(t, "v6.example.com")
	fakeFrontIPv6(t, neverDiscover(t), func(context.Context, string, int) string { return "" })
	fakeFrontName6(t, func(context.Context, string, string) string {
		if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "us").
			Updates(map[string]any{"public_name6": "ipv6.example.com", "address6_error": address6NotChecked}).Error; err != nil {
			t.Fatal(err)
		}
		return name6AlsoA
	})
	(&RelayService{}).refreshFrontIPv6(front)
	if got := readRelay(t, "us"); got.PublicName6 != "ipv6.example.com" || got.Address6Error != address6NotChecked {
		t.Errorf("stored name %q error %q; want the typed ipv6.example.com unchecked", got.PublicName6, got.Address6Error)
	}
}

func fakeProbe(t *testing.T, budget time.Duration, reach func(context.Context, relay.Server) (relay.Reachability, error)) {
	t.Helper()
	savedReach, savedBudget := probeRelay, relayProbeTimeout
	probeRelay, relayProbeTimeout = reach, budget
	t.Cleanup(func() { probeRelay, relayProbeTimeout = savedReach, savedBudget })
}

func answeredProbe(context.Context, relay.Server) (relay.Reachability, error) {
	return relay.Reachability{Country: "US"}, nil
}

// A front set up before IPv6 existed learns its address from a probe; an
// exit's IPv6 carries no customer and is never asked for.
func TestAProbeLearnsAFrontsIPv6(t *testing.T) {
	t.Run("front", func(t *testing.T) {
		front := seedIPv6Front(t, model.RelayFront, "", "")
		fakeProbe(t, time.Minute, answeredProbe)
		fakeFrontIPv6(t,
			func(context.Context, relay.Credentials) (string, error) { return "2a01:4f8:c17:1234::1", nil },
			func(context.Context, string, int) string { return "" })
		_, _ = (&RelayService{}).Probe(front.Id)
		if got := readRelay(t, "us"); got.Address6 != "2a01:4f8:c17:1234::1" || got.Address6Error != "" {
			t.Errorf("after a probe the front has address6 %q error %q; want 2a01:4f8:c17:1234::1 reached", got.Address6, got.Address6Error)
		}
	})
	t.Run("exit", func(t *testing.T) {
		exit := seedIPv6Front(t, model.RelayExit, "", "")
		fakeProbe(t, time.Minute, answeredProbe)
		fakeFrontIPv6(t, neverDiscover(t), neverDial(t))
		_, _ = (&RelayService{}).Probe(exit.Id)
	})
}

// The reach script can spend the probe's whole budget on a filtered front; the
// IPv6 check then still gets a clock of its own instead of an expired one.
func TestAProbeThatUsedItsBudgetStillChecksTheIPv6(t *testing.T) {
	front := seedIPv6Front(t, model.RelayFront, "", "")
	fakeProbe(t, 20*time.Millisecond, func(ctx context.Context, _ relay.Server) (relay.Reachability, error) {
		<-ctx.Done()
		return relay.Reachability{Country: "IR"}, nil
	})
	fakeFrontIPv6(t,
		func(ctx context.Context, _ relay.Credentials) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("discovery ran on the probe's clock: %w", err)
			}
			return "2a01:4f8:c17:1234::1", nil
		},
		func(ctx context.Context, address6 string, _ int) string {
			if ctx.Err() != nil {
				return "no answer from [" + address6 + "]:443 within 5s"
			}
			return ""
		})
	_, _ = (&RelayService{}).Probe(front.Id)
	if got := readRelay(t, "us"); got.Address6 != "2a01:4f8:c17:1234::1" || got.Address6Error != "" {
		t.Errorf("stored address6 %q error %q; want 2a01:4f8:c17:1234::1 reached", got.Address6, got.Address6Error)
	}
}

// A probe that could not get onto the front does not wait out a second SSH dial
// for discovery; an IPv6 already known is still dialled, since that needs no SSH.
func TestAProbeThatCouldNotSSHOnlyDialsAKnownIPv6(t *testing.T) {
	sshDown := func(context.Context, relay.Server) (relay.Reachability, error) {
		return relay.Reachability{}, errors.New("dial tcp 198.51.100.5:22: i/o timeout")
	}
	t.Run("no address yet", func(t *testing.T) {
		front := seedIPv6Front(t, model.RelayFront, "", "")
		fakeProbe(t, time.Minute, sshDown)
		fakeFrontIPv6(t, neverDiscover(t), neverDial(t))
		_, _ = (&RelayService{}).Probe(front.Id)
	})
	t.Run("a known address", func(t *testing.T) {
		front := seedIPv6Front(t, model.RelayFront, "2001:db8::1", address6NotChecked)
		fakeProbe(t, time.Minute, sshDown)
		fakeFrontIPv6(t, neverDiscover(t), func(context.Context, string, int) string { return "" })
		_, _ = (&RelayService{}).Probe(front.Id)
		if got := readRelay(t, "us"); got.Address6Error != "" {
			t.Errorf("the known IPv6 was not dialled: error %q", got.Address6Error)
		}
	})
}
