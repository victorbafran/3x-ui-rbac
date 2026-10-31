package ocserv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configFor(t *testing.T, inst Instance) string {
	t.Helper()
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	return renderConfig(inst, "/tmp/cert.pem", "/tmp/key.pem")
}

// The directives below are not decoration: each one was needed to get a real
// AnyConnect client through a handshake on ocserv 1.2.4.
func TestRenderConfigCarriesWhatTheClientNeeds(t *testing.T) {
	cfg := configFor(t, Instance{Id: 3, Tag: "cisco-1", Port: 443})

	for _, want := range []string{
		"tcp-port = 443",
		"udp-port = 443",
		"cisco-client-compat = true",
		"use-occtl = true",
		"server-cert = /tmp/cert.pem",
		"server-key = /tmp/key.pem",
		"tunnel-all-dns = true",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config is missing %q:\n%s", want, cfg)
		}
	}
}

// The occtl socket is a separate directive from the worker IPC socket. Setting
// only socket-file leaves occtl unable to reach the server at all, which takes
// out both traffic accounting and the ability to disconnect a client.
func TestRenderConfigSetsBothSocketsSeparately(t *testing.T) {
	cfg := configFor(t, Instance{Id: 3, Port: 443})
	if !strings.Contains(cfg, "occtl-socket-file = ") {
		t.Fatalf("occtl-socket-file is absent; occtl would not reach the server:\n%s", cfg)
	}
	ipc := lineValue(cfg, "socket-file = ")
	ctl := lineValue(cfg, "occtl-socket-file = ")
	if ipc == "" || ctl == "" || ipc == ctl {
		t.Fatalf("the two sockets are not distinct: ipc=%q ctl=%q", ipc, ctl)
	}
}

func lineValue(cfg, prefix string) string {
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// Two cisco inbounds on one host must not hand out the same addresses, or each
// would route the other's clients.
func TestDefaultPoolDiffersPerInbound(t *testing.T) {
	seen := map[string]int{}
	for id := 1; id <= 300; id++ {
		pool := DefaultPool(id)
		if other, clash := seen[pool]; clash {
			t.Fatalf("inbounds %d and %d share the pool %s", other, id, pool)
		}
		seen[pool] = id
	}
}

func TestCIDRRendersThePoolAsAPrefix(t *testing.T) {
	cases := []struct {
		network, netmask, want string
	}{
		{"10.8.0.0", "255.255.255.0", "10.8.0.0/24"},
		{"10.8.0.0", "255.255.0.0", "10.8.0.0/16"},
		{"10.8.0.0", "", "10.8.0.0/24"},
	}
	for _, tc := range cases {
		inst := Instance{Id: 1, Network: tc.network, Netmask: tc.netmask}
		if got := inst.CIDR(); got != tc.want {
			t.Fatalf("CIDR(%s/%s) = %s, want %s", tc.network, tc.netmask, got, tc.want)
		}
	}
}

// A restart drops every live session, so the structural fingerprint must move
// only for things ocserv reads once at startup.
func TestStructuralFingerprintIgnoresClientEdits(t *testing.T) {
	base := Instance{Id: 1, Port: 443, Users: []User{{Name: "ann", Login: "ann", Password: "a"}}}
	withMore := base
	withMore.Users = append(append([]User{}, base.Users...), User{Name: "bob", Login: "bob", Password: "b"})

	if base.structuralFingerprint() != withMore.structuralFingerprint() {
		t.Fatal("adding a client moved the structural fingerprint, which would restart ocserv and drop everyone online")
	}
	if base.usersFingerprint() == withMore.usersFingerprint() {
		t.Fatal("adding a client left the user fingerprint unchanged, so the password file would never be rewritten")
	}
}

func TestStructuralFingerprintMovesWithThePort(t *testing.T) {
	a := Instance{Id: 1, Port: 443}
	b := Instance{Id: 1, Port: 8443}
	if a.structuralFingerprint() == b.structuralFingerprint() {
		t.Fatal("a port change did not move the structural fingerprint, so ocserv would keep the old port")
	}
}

// A reordered clients array in the stored settings is not a change.
func TestUsersFingerprintIgnoresOrder(t *testing.T) {
	ann := User{Name: "ann", Login: "ann", Password: "a"}
	bob := User{Name: "bob", Login: "bob", Password: "b"}
	one := Instance{Id: 1, Users: []User{ann, bob}}
	two := Instance{Id: 1, Users: []User{bob, ann}}
	if one.usersFingerprint() != two.usersFingerprint() {
		t.Fatal("reordering the clients read as a change; the password file would be rewritten on every reconcile")
	}
}

// A password change has to reach the file, or the customer keeps logging in
// with the old one.
func TestUsersFingerprintMovesWithAPassword(t *testing.T) {
	a := Instance{Id: 1, Users: []User{{Name: "ann", Login: "ann", Password: "old"}}}
	b := Instance{Id: 1, Users: []User{{Name: "ann", Login: "ann", Password: "new"}}}
	if a.usersFingerprint() == b.usersFingerprint() {
		t.Fatal("a password change did not move the user fingerprint")
	}
}

func TestLoginToEmailMapsBackToTheBillingIdentity(t *testing.T) {
	inst := Instance{Id: 1, Users: []User{
		{Name: "a9:customer@example.com", Login: "a9_customer@example.com", Password: "p"},
	}}
	m := inst.LoginToEmail()
	if got := m["a9_customer@example.com"]; got != "a9:customer@example.com" {
		t.Fatalf("login mapped back to %q, want the namespaced email", got)
	}
}

// Measured against a stock ocserv on the same server, over the same path, in
// the same minute: writing this option cost 223 Mbit/s of download and turned
// a clean 50 Mbit/s UDP stream into 87% loss. 4, 23, 32, 64 and 1000 all
// behaved alike, around 5 Mbit/s, so there is no safe value to offer and the
// only correct rendering is no line at all.
func TestRenderConfigNeverWritesAnOutputBuffer(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443})
	if strings.Contains(cfg, "output-buffer") {
		t.Fatalf("output-buffer was written; it caps the tunnel near 5 Mbit/s:\n%s", cfg)
	}
}

// Path MTU discovery needs ICMP to come back. On a path that swallows it the
// tunnel guesses high and then stalls on every large packet, which is worse
// than a conservative fixed MTU. ocserv's own shipped config has it off.
func TestMtuDiscoveryIsOffWithAFixedMtuInstead(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443})
	if !strings.Contains(cfg, "try-mtu-discovery = false") {
		t.Fatalf("discovery is on by default:\n%s", cfg)
	}
	if !strings.Contains(cfg, "mtu = 1400") {
		t.Fatalf("no fixed MTU to fall back on:\n%s", cfg)
	}
}

// An operator whose network carries ICMP properly can have the larger
// packets back, and then a fixed MTU must not be written alongside.
func TestMtuDiscoveryCanBeTurnedBackOn(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443, MTUDiscovery: true})
	if !strings.Contains(cfg, "try-mtu-discovery = true") {
		t.Fatalf("discovery was not honoured:\n%s", cfg)
	}
	if strings.Contains(cfg, "\nmtu = ") {
		t.Fatalf("a fixed MTU was written even though discovery is on:\n%s", cfg)
	}
}

// mobile-dpd is how long a mobile session nobody is hearing from is kept.
// Default thirty minutes is right on a public address; behind a front, where
// every client is 127.0.0.1 and a returning phone cannot be recognised, it
// has to be cut right down or ghosts pile up and fill the account.
func TestMobileDpdDefaultsHighAndCanBeCutForATunnel(t *testing.T) {
	def := configFor(t, Instance{Id: 1, Port: 443})
	if !strings.Contains(def, "mobile-dpd = 1800") {
		t.Fatalf("the default mobile-dpd is not 1800:\n%s", def)
	}
	low := configFor(t, Instance{Id: 1, Port: 443, MobileDPD: 90})
	if !strings.Contains(low, "mobile-dpd = 90") {
		t.Fatalf("a behind-tunnel inbound could not cut mobile-dpd:\n%s", low)
	}
	if strings.Contains(low, "mobile-dpd = 1800") {
		t.Fatalf("the default was written alongside the override:\n%s", low)
	}
}

// These are read once at startup, so a change has to restart the instance —
// a rewrite of the password file alone would leave the old values running.
func TestTuningMovesTheStructuralFingerprint(t *testing.T) {
	base := Instance{Id: 1, Port: 443}
	for _, tc := range []struct {
		name string
		inst Instance
	}{
		{"mtu", Instance{Id: 1, Port: 443, MTU: 1300}},
		{"discovery", Instance{Id: 1, Port: 443, MTUDiscovery: true}},
		{"mobile-dpd", Instance{Id: 1, Port: 443, MobileDPD: 90}},
	} {
		if base.structuralFingerprint() == tc.inst.structuralFingerprint() {
			t.Errorf("%s did not move the structural fingerprint; ocserv would keep the old value", tc.name)
		}
	}
}

// A certificate renewed at the same path is the case that bit on a real
// server: the path never moved, so ocserv kept serving the old certificate.
// The new content alone has to restart it, and the same content must not.
func TestACertificateRenewedInPlaceRestartsTheInstance(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "fullchain.pem")
	key := filepath.Join(dir, "privkey.pem")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(cert, "certificate one")
	write(key, "key one")
	inst := Instance{Id: 1, Port: 443, CertFile: cert, KeyFile: key}
	before := inst.structuralFingerprint()
	if inst.structuralFingerprint() != before {
		t.Fatal("an untouched certificate moved the fingerprint; every reconcile would restart ocserv")
	}
	write(cert, "certificate two")
	write(key, "key two")
	if inst.structuralFingerprint() == before {
		t.Fatal("a certificate renewed in place left the fingerprint alone; ocserv would keep the old one")
	}
}

// DTLS is what makes this protocol fast. By default it rides the same port
// as TCP, which is what an AnyConnect client expects.
func TestUdpSharesTheTcpPortByDefault(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443})
	if !strings.Contains(cfg, "udp-port = 443") {
		t.Fatalf("no DTLS port alongside TCP:\n%s", cfg)
	}
}

// When that UDP port is blocked and another is not, the operator moves it
// without moving the TCP port the client connects to.
func TestUdpCanUseItsOwnPort(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443, UDPPort: 8443})
	if !strings.Contains(cfg, "udp-port = 8443") || !strings.Contains(cfg, "tcp-port = 443") {
		t.Fatalf("the two ports did not separate:\n%s", cfg)
	}
}

// With UDP blocked for good, offering DTLS costs every connection the
// switch-to-tcp timeout and then falls back anyway. Omitting udp-port is
// what stops the client waiting.
func TestDisablingUdpRemovesThePortEntirely(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443, DisableUDP: true})
	if strings.Contains(cfg, "udp-port") {
		t.Fatalf("udp-port is still offered although UDP is disabled:\n%s", cfg)
	}
	if !strings.Contains(cfg, "tcp-port = 443") {
		t.Fatalf("TCP went away with it:\n%s", cfg)
	}
}

func TestUdpChangesMoveTheStructuralFingerprint(t *testing.T) {
	base := Instance{Id: 1, Port: 443}
	moved := Instance{Id: 1, Port: 443, UDPPort: 8443}
	off := Instance{Id: 1, Port: 443, DisableUDP: true}
	if base.structuralFingerprint() == moved.structuralFingerprint() {
		t.Error("moving the UDP port did not move the fingerprint")
	}
	if base.structuralFingerprint() == off.structuralFingerprint() {
		t.Error("disabling UDP did not move the fingerprint")
	}
}

// The header names the customer, so anyone able to dial in direct could forge
// one; real client IP therefore listens where only an entry server reaches.
func TestRealClientIPReadsTheHeaderOnLoopbackOnly(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443, Listen: "0.0.0.0", ProxyProtocol: true})
	for _, want := range []string{"listen-host = 127.0.0.1\n", "listen-proxy-proto = true\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	plain := configFor(t, Instance{Id: 1, Port: 443, Listen: "0.0.0.0"})
	if strings.Contains(plain, "listen-proxy-proto") || !strings.Contains(plain, "listen-host = 0.0.0.0\n") {
		t.Errorf("an ordinary inbound was told to expect a header:\n%s", plain)
	}
	loop := Instance{Id: 1, Port: 443, Listen: "127.0.0.1"}
	withHeader := loop
	withHeader.ProxyProtocol = true
	if loop.structuralFingerprint() == withHeader.structuralFingerprint() {
		t.Error("switching real client IP does not restart ocserv, which reads it once")
	}
}

func hostWithIPv6(t *testing.T, has bool) {
	t.Helper()
	saved := hostHasIPv6
	hostHasIPv6 = func() bool { return has }
	t.Cleanup(func() { hostHasIPv6 = saved })
}

// listen-host = 0.0.0.0 left an IPv6 address no way in; with none written ocserv
// listens on every address of both families. An explicit address stays as typed.
func TestAnInboundWithNoListenAddressServesBothFamilies(t *testing.T) {
	cases := []struct {
		name string
		ipv6 bool
		inst Instance
		want string
	}{
		{"no listen", true, Instance{Id: 1, Port: 443}, ""},
		{"explicit IPv4 listen", true, Instance{Id: 1, Port: 443, Listen: "203.0.113.7"}, "listen-host = 203.0.113.7\n"},
		{"real client IP", true, Instance{Id: 1, Port: 443, ProxyProtocol: true}, "listen-host = 127.0.0.1\n"},
		{"host without IPv6", false, Instance{Id: 1, Port: 443}, "listen-host = 0.0.0.0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hostWithIPv6(t, tc.ipv6)
			cfg := configFor(t, tc.inst)
			if tc.want == "" && strings.Contains(cfg, "listen-host") {
				t.Fatalf("config pins a listen address:\n%s", cfg)
			}
			if tc.want != "" && !strings.Contains(cfg, tc.want) {
				t.Fatalf("config lacks %q:\n%s", tc.want, cfg)
			}
		})
	}
}

// Without an IPv6 address a client keeps its own, and a client that ignores the
// bypass-protocol hint sends IPv6 straight past the tunnel; each inbound gets
// its own private /64, so the client's IPv6 goes into the tunnel instead.
func TestEveryClientGetsAPrivateIPv6AddressSoNoneLeaks(t *testing.T) {
	one := configFor(t, Instance{Id: 1, Port: 443})
	six := configFor(t, Instance{Id: 6, Port: 8443})
	for cfg, want := range map[string]string{one: "fd5a:3c71:9e04:1::/64", six: "fd5a:3c71:9e04:6::/64"} {
		if !strings.Contains(cfg, "ipv6-network = "+want+"\n") || !strings.Contains(cfg, "ipv6-subnet-prefix = 128\n") {
			t.Errorf("config lacks ipv6-network %s with a /128 per client:\n%s", want, cfg)
		}
	}
}

// A restore can replace the pair ocserv made for itself; it is read once at
// start, so the change has to restart it.
func TestARestoredGeneratedPairRestartsOcserv(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	inst := Instance{Id: 6, Port: 8443}
	for _, p := range []string{certPathForID(6), keyPathForID(6)} {
		if err := os.WriteFile(p, []byte("made here"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := inst.structuralFingerprint()
	for _, p := range []string{certPathForID(6), keyPathForID(6)} {
		saved, _ := os.ReadFile(p)
		if err := os.WriteFile(p, []byte("restored"), 0o600); err != nil {
			t.Fatal(err)
		}
		if inst.structuralFingerprint() == before {
			t.Errorf("%s was replaced and ocserv would keep the old one", filepath.Base(p))
		}
		if err := os.WriteFile(p, saved, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
