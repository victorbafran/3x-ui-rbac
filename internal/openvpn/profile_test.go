package openvpn

import (
	"os"
	"strings"
	"testing"
)

// profileFor stands an instance's generated material up in a temporary
// directory, the way the manager does before anyone asks for a profile.
func profileFor(t *testing.T, inst Instance, p Profile) string {
	t.Helper()
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatalf("generating the keypair: %v", err)
	}
	if _, err := ensureTLSCryptKey(inst.Id); err != nil {
		t.Fatalf("generating the shared key: %v", err)
	}
	out, err := Render(inst, p)
	if err != nil {
		t.Fatalf("rendering the profile: %v", err)
	}
	return out
}

// A profile that points at files beside it is one a customer has to install
// rather than open, and most will not manage it. Everything is inside.
func TestTheProfileCarriesEverythingInsideIt(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "vpn.example.com", Label: "Germany"})
	for _, want := range []string{"<ca>", "</ca>", "<tls-crypt>", "</tls-crypt>", "BEGIN CERTIFICATE", "OpenVPN Static key"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the profile:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"ca /var", "tls-crypt /var", "cert /var"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the profile points at a file on the server (%q), which the customer does not have", unwanted)
		}
	}
}

// Without this line the client never asks for a name and password and fails
// with a handshake error that says nothing about credentials.
func TestTheClientKnowsToAskForAPassword(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "198.51.100.1"})
	if !strings.Contains(out, "auth-user-pass") {
		t.Fatalf("the client would not ask for credentials:\n%s", out)
	}
	if !strings.Contains(out, "remote-cert-tls server") {
		t.Fatalf("the client would accept any certificate it is handed:\n%s", out)
	}
}

// A push is a request the client may ignore. These two close the ways a
// customer's real address and browsing escape, so they are in the customer's
// own file as well.
func TestTheLeakGuardsAreInTheCustomersFileToo(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "198.51.100.1"})
	for _, want := range []string{"block-ipv6", "block-outside-dns"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is only pushed, not in the profile, so a client that ignores the push leaks:\n%s", want, out)
		}
	}
}

// The server says tcp-server and the client says tcp-client. A profile
// carrying the server's spelling refuses to start, with an error about an
// unknown protocol rather than about the profile.
func TestTheClientSpellsTheProtocolItsOwnWay(t *testing.T) {
	udp := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "a.example.com"})
	if !strings.Contains(udp, "proto udp") {
		t.Errorf("udp profile is wrong:\n%s", udp)
	}
	tcp := profileFor(t, Instance{Id: 2, Port: 443, UseTCP: true}, Profile{Address: "a.example.com"})
	if !strings.Contains(tcp, "proto tcp-client") {
		t.Errorf("tcp profile does not use the client spelling:\n%s", tcp)
	}
	if strings.Contains(tcp, "tcp-server") {
		t.Errorf("the profile carries the server's spelling:\n%s", tcp)
	}
}

// The address and port are the one thing a customer cannot supply themselves.
func TestTheProfileSaysWhereToConnect(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1195}, Profile{Address: "vpn.example.com"})
	if !strings.Contains(out, "remote vpn.example.com 1195") {
		t.Fatalf("the profile does not say where to connect:\n%s", out)
	}
}

// Asked for a profile before the inbound has ever started, the answer has to
// say what is missing rather than hand out a file with an empty certificate
// in it. Each piece is checked on its own: with both absent, a test that only
// asked "did it fail" would pass with either guard taken out.
func TestAProfileBeforeTheInboundStartsIsRefused(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	inst := Instance{Id: 1, Port: 1194}

	_, err := Render(inst, Profile{Address: "a.example.com"})
	if err == nil {
		t.Fatal("a profile was produced with nothing behind it")
	}
	if !strings.Contains(err.Error(), "certificate authority") {
		t.Fatalf("the missing authority was not what it complained about: %v", err)
	}

	// Now the authority exists and only the shared key is missing.
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	_, err = Render(inst, Profile{Address: "a.example.com"})
	if err == nil {
		t.Fatal("a profile was produced with no shared key behind it")
	}
	if !strings.Contains(err.Error(), "shared key") {
		t.Fatalf("the missing shared key was not what it complained about: %v", err)
	}
}

// A profile with nowhere to connect is worse than no profile: the customer
// opens it, it fails, and nothing says why.
func TestAProfileWithNoAddressIsRefused(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	inst := Instance{Id: 1, Port: 1194}
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTLSCryptKey(inst.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(inst, Profile{Address: "   "}); err == nil {
		t.Fatal("a profile with no address was produced")
	}
}

// A customer with several countries has several files, and three called
// client.ovpn tell them nothing about which is which.
func TestTheFileIsNamedAfterItsCountry(t *testing.T) {
	for label, want := range map[string]string{
		"Netherlands":    "Netherlands.ovpn",
		"United States":  "United-States.ovpn",
		"🇫🇷 France":      "France.ovpn",
		"../../etc/pass": "etcpass.ovpn",
		"":               "vpn.ovpn",
	} {
		if got := FileName(label); got != want {
			t.Errorf("%q became %q, wanted %q", label, got, want)
		}
	}
}

// The certificate is generated once and reused. A new one on every call would
// look to every client like a different server, and the client refuses it —
// which reads to the customer as the VPN having broken.
func TestTheCertificateIsNotReissuedEveryTime(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	inst := Instance{Id: 4, Port: 1194}
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(certPathForID(4))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(certPathForID(4))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("the certificate was reissued, so every client that pinned the old one would refuse to connect")
	}
}
