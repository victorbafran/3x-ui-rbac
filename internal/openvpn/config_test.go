package openvpn

import (
	"strings"
	"testing"
)

func configFor(t *testing.T, inst Instance) string {
	t.Helper()
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	// The same string the manager builds. Passing a simpler one here is how
	// the unquoted helper line reached a customer's server: the test proved
	// something true of a value production never produces.
	return renderConfig(inst, HelperCommand("/usr/local/x-ui/x-ui", inst.Id))
}

// The cisco side lost 223 Mbit/s of download to one line that the manual said
// would improve throughput. OpenVPN has the same line under two names, and
// guides everywhere tell you to set them. They are never written.
func TestTheSocketBuffersAreLeftToTheKernel(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	for _, forbidden := range []string{"sndbuf", "rcvbuf"} {
		if strings.Contains(cfg, forbidden) {
			t.Fatalf("%s was written; it pins the socket buffer and turns off the kernel's own sizing:\n%s", forbidden, cfg)
		}
	}
}

// Compression is deprecated, is a known weakness, and costs more than it
// saves on a fast link. None of its spellings belongs here.
func TestNothingIsCompressed(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	for _, forbidden := range []string{"comp-lzo", "compress", "allow-compression"} {
		if strings.Contains(cfg, forbidden) {
			t.Fatalf("%s was written:\n%s", forbidden, cfg)
		}
	}
}

// Three ways a customer's real address or browsing escapes around the tunnel,
// none of which OpenVPN closes on its own. Each line here is one of them.
func TestTheClientIsToldNotToLeak(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194, DNS: []string{"9.9.9.9"}})
	for what, want := range map[string]string{
		"traffic would not go through the tunnel at all": `push "redirect-gateway def1 bypass-dhcp"`,
		"names would be looked up outside the tunnel":    `push "dhcp-option DNS 9.9.9.9"`,
		"windows would keep its own resolver":            `push "block-outside-dns"`,
		"ipv6 would leave with the real address on it":   `push "block-ipv6"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("%s: %q is missing from the config", what, want)
		}
	}
}

// A certificate the panel reissued on every restart would look to every
// client like a different server, and the client would refuse it. The config
// must point at a stable path, not at something generated inline.
func TestTheConfigCarriesWhatAClientVerifies(t *testing.T) {
	cfg := configFor(t, Instance{Id: 3, Port: 1194})
	for _, want := range []string{"ca ", "cert ", "key ", "tls-crypt ", "tls-version-min 1.2"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("%q is missing:\n%s", want, cfg)
		}
	}
}

// The customer types a name and a password; there is no per-client
// certificate. Each of these three lines is needed for that to work at all,
// and leaving one out fails in a way that reads as a wrong password.
func TestTheCustomerCanLogInWithAPassword(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	for _, want := range []string{
		"verify-client-cert none",
		"username-as-common-name",
		"script-security 2",
		`auth-user-pass-verify "/usr/local/x-ui/x-ui openvpn-verify 1" via-file`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("%q is missing:\n%s", want, cfg)
		}
	}
}

// Renegotiation asks the helper again mid-session. With a customer whose
// password has just been changed, that throws them off a working connection.
func TestASessionIsNotReauthenticatedMidway(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	if !strings.Contains(cfg, "reneg-sec 0") {
		t.Fatalf("the session would be re-authenticated while it is running:\n%s", cfg)
	}
}

// The management interface can disconnect any customer. On a localhost port
// every process on the host could reach it; on a unix socket only what the
// file permissions allow.
func TestTheManagementInterfaceIsNotOnAPort(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	if !strings.Contains(cfg, "unix") || !strings.Contains(cfg, "management ") {
		t.Fatalf("the management interface is not on a unix socket:\n%s", cfg)
	}
	if strings.Contains(cfg, "management 127.0.0.1") {
		t.Fatalf("the management interface is on a localhost port, which every process can reach:\n%s", cfg)
	}
}

// UDP is the default because it is faster; TCP exists for a network that
// drops UDP outright, and the exit notification only means anything on UDP.
func TestTcpIsOfferedWithoutTheUdpOnlyOptions(t *testing.T) {
	udp := configFor(t, Instance{Id: 1, Port: 1194})
	if !strings.Contains(udp, "proto udp") || !strings.Contains(udp, "explicit-exit-notify 1") {
		t.Fatalf("the udp inbound is wrong:\n%s", udp)
	}
	tcp := configFor(t, Instance{Id: 1, Port: 443, UseTCP: true})
	if !strings.Contains(tcp, "proto tcp-server") {
		t.Fatalf("the tcp inbound did not switch protocol:\n%s", tcp)
	}
	if strings.Contains(tcp, "explicit-exit-notify") {
		t.Fatalf("explicit-exit-notify was written for tcp, where openvpn refuses it:\n%s", tcp)
	}
}

// Two inbounds handing out the same addresses would route each other's
// customers, and an OpenVPN pool must also stay clear of the cisco pools and
// of the block the exit servers use.
func TestPoolsCannotCollideWithAnythingElse(t *testing.T) {
	seen := map[string]int{}
	for id := 0; id < 512; id++ {
		pool := DefaultPool(id)
		if other, clash := seen[pool]; clash {
			t.Fatalf("inbounds %d and %d are both given %s", other, id, pool)
		}
		seen[pool] = id

		octet := strings.Split(pool, ".")[1]
		// cisco walks 10.8 to 10.247 and exit locations take 10.248 upward.
		if octet == "0" || octet >= "8" {
			t.Fatalf("inbound %d is given %s, which is inside the range cisco or an exit location uses", id, pool)
		}
	}
}

// The fingerprint decides whether a change restarts the process and drops
// every live session. Everything the process only reads at startup has to be
// in it, and the client list must not be.
func TestOnlyAStartupChangeRestartsTheProcess(t *testing.T) {
	base := Instance{Id: 1, Port: 1194}
	for _, tc := range []struct {
		name string
		inst Instance
	}{
		{"the port", Instance{Id: 1, Port: 1195}},
		{"the protocol", Instance{Id: 1, Port: 1194, UseTCP: true}},
		{"the pool", Instance{Id: 1, Port: 1194, Network: "10.5.5.0"}},
		{"the resolvers", Instance{Id: 1, Port: 1194, DNS: []string{"9.9.9.9"}}},
		{"the exit", Instance{Id: 1, Port: 1194, Exit: "wgr2"}},
		{"the device limit", Instance{Id: 1, Port: 1194, MaxSameClients: 5}},
	} {
		if base.structuralFingerprint() == tc.inst.structuralFingerprint() {
			t.Errorf("%s did not move the fingerprint, so the running process would keep the old value", tc.name)
		}
	}

	withUsers := base
	withUsers.Users = []User{{Name: "a@b", Login: "a@b", Password: "x"}}
	if base.structuralFingerprint() != withUsers.structuralFingerprint() {
		t.Error("adding a client moved the structural fingerprint, which would drop every live session to let one customer in")
	}
}

// openvpn allows auth-user-pass-verify at most two parameters, and counts
// them by whitespace. The helper is a binary plus a subcommand plus an
// inbound id — four words before the method — so without quotes the server
// refuses to start and the inbound never appears. This is that rule, spelled
// out, rather than a comparison against one expected line: the binary's path
// and the id both vary, and the quoting is the part that must not.
func TestTheHelperAndItsArgumentsAreOneParameter(t *testing.T) {
	for _, id := range []int{1, 3, 42} {
		cfg := configFor(t, Instance{Id: id, Port: 1194})
		var line string
		for _, l := range strings.Split(cfg, "\n") {
			if strings.HasPrefix(l, "auth-user-pass-verify") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("inbound %d: no password check at all; anyone could log in:\n%s", id, cfg)
		}
		rest := strings.TrimPrefix(line, "auth-user-pass-verify ")
		if !strings.HasPrefix(rest, `"`) || !strings.HasSuffix(rest, `" via-file`) {
			t.Errorf("inbound %d: openvpn would read this as more than two parameters and exit at startup: %s", id, line)
		}
	}
}
