package service

import (
	"strings"
	"testing"
)

// A relay's name becomes a group in a VPN server's config, a filename, and a
// field in a colon-separated file. Letting a separator through would let one
// country rewrite another's entry, so it is refused at the form.
func TestARelayNameIsCheckedTheWayACountryNameIs(t *testing.T) {
	s := &RelayService{}
	for _, bad := range []string{"", "de:nl", "de,nl", "../etc", "de nl"} {
		if _, err := s.rowFrom(RelayRequest{Name: bad, Address: "198.51.100.1"}, nil); err == nil {
			t.Errorf("%q was accepted as a relay name", bad)
		}
	}
	if _, err := s.rowFrom(RelayRequest{Name: "netherlands", Address: "198.51.100.1"}, nil); err != nil {
		t.Errorf("an ordinary name was refused: %v", err)
	}
}

// A relay with no address is a row that can never be reached, and the failure
// would only show up minutes later as a connection timeout.
func TestARelayNeedsAnAddress(t *testing.T) {
	s := &RelayService{}
	if _, err := s.rowFrom(RelayRequest{Name: "nl", Address: "   "}, nil); err == nil {
		t.Fatal("a relay with no address was accepted")
	}
}

// The fields an operator leaves alone should mean what they look like they
// mean: ssh on 22, as root.
func TestTheBlankFieldsTakeTheUsualDefaults(t *testing.T) {
	s := &RelayService{}
	r, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.SSHPort != 22 {
		t.Errorf("blank port became %d", r.SSHPort)
	}
	if r.SSHUser != "root" {
		t.Errorf("blank user became %q", r.SSHUser)
	}
	if !r.Enabled {
		t.Error("a new relay was created switched off")
	}
	if r.Tunnel != "wireguard" {
		t.Errorf("tunnel kind is %q", r.Tunnel)
	}
}

// Editing a relay must not silently switch it off, or on: whatever the
// operator did not touch stays as it was.
func TestAnEditKeepsWhatTheOperatorDidNotChange(t *testing.T) {
	s := &RelayService{}
	base, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Id = 7
	base.Enabled = false
	base.KeyPath = "/etc/x-ui/relay-keys/relay-7"

	edited, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.2"}, base)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Enabled {
		t.Error("editing the address turned a disabled relay back on")
	}
	if edited.KeyPath != base.KeyPath {
		t.Errorf("the enrolled key was dropped on an edit: %q", edited.KeyPath)
	}
	if edited.Id != 7 {
		t.Errorf("the edit lost the row it was editing: id %d", edited.Id)
	}
}

// The stored tunnel key must come back exactly, or the tunnel it belongs to
// never handshakes again and the country goes quiet with nothing to explain it.
func TestTheStoredTunnelKeyRoundTrips(t *testing.T) {
	stored, err := encryptRelaySecret(3, "a-private-key")
	if err != nil {
		t.Fatal(err)
	}
	back, err := decryptRelaySecret(3, stored)
	if err != nil {
		t.Fatal(err)
	}
	if back != "a-private-key" {
		t.Fatalf("the key came back as %q", back)
	}
}

// A relay with no key at all has to say so rather than hand an empty string to
// WireGuard, which would bring up an interface that silently never works.
func TestAMissingTunnelKeyIsRefused(t *testing.T) {
	if _, err := decryptRelaySecret(3, ""); err == nil {
		t.Fatal("an empty tunnel key was accepted")
	} else if !strings.Contains(err.Error(), "enroll it again") {
		t.Fatalf("the error does not say what to do: %v", err)
	}
}

// The list of inbounds offering a relay is shown to the operator before they
// delete it, so the same inbound appearing twice would read as two.
func TestAnInboundIsListedOnceHoweverManyCountriesItOffers(t *testing.T) {
	got := appendOnce(appendOnce(nil, "cisco-1"), "cisco-1")
	if len(got) != 1 {
		t.Fatalf("expected one entry, got %v", got)
	}
	got = appendOnce(got, "cisco-2")
	if strings.Join(got, ",") != "cisco-1,cisco-2" {
		t.Fatalf("a second inbound was lost: %v", got)
	}
}

// A relay nobody uses yet must come back as an empty list, not as nothing. Go
// marshals a nil slice to null, the page reads .length off it, and on a real
// panel that took the whole page down the first time it was opened.
func TestARelayNobodyUsesStillGetsAList(t *testing.T) {
	got := inUse(map[string][]string{}, "wgr1")
	if got == nil {
		t.Fatal("an unused relay was given a nil list, which reaches the browser as null")
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty list, got %v", got)
	}
	if used := inUse(map[string][]string{"wgr1": {"cisco-1"}}, "wgr1"); len(used) != 1 {
		t.Fatalf("a relay in use lost its inbound: %v", used)
	}
}

// A blank role is an exit, because that is what every relay was before fronts
// existed and what a client that predates the field still sends.
func TestABlankRoleIsAnExit(t *testing.T) {
	s := &RelayService{}
	r, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.1"}, nil)
	if err != nil {
		t.Fatalf("rowFrom: %v", err)
	}
	if r.Role != "exit" {
		t.Fatalf("role = %q, want exit for a relay with no role set", r.Role)
	}
}

// A role that is neither exit nor front is a typo, and storing it would make a
// third kind of relay nothing handles.
func TestANonsenseRoleIsRefused(t *testing.T) {
	s := &RelayService{}
	_, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.1", Role: "both"}, nil)
	if err == nil {
		t.Fatal("a nonsense role was accepted")
	}
	if !strings.Contains(err.Error(), "exit or a front") {
		t.Fatalf("refused for some other reason: %v", err)
	}
}

// A front gets the tunnel defaults when the operator leaves them blank, and
// its transport is validated.
func TestAFrontTakesTheTunnelDefaults(t *testing.T) {
	s := &RelayService{}
	r, err := s.rowFrom(RelayRequest{Name: "ir", Address: "185.0.0.1", Role: "front"}, nil)
	if err != nil {
		t.Fatalf("rowFrom: %v", err)
	}
	if r.TunnelPort != 3080 {
		t.Errorf("tunnel port = %d, want the default 3080", r.TunnelPort)
	}
	if r.Transport != "tcp" {
		t.Errorf("transport = %q, want the default tcp", r.Transport)
	}
	if r.Tunnel != "backhaul" {
		t.Errorf("tunnel = %q, want backhaul for a front", r.Tunnel)
	}
}

// A transport Backhaul does not know would produce a tunnel that refuses to
// start with nothing in the panel to say why, so it is caught at the form.
func TestAFrontWithAnUnknownTransportIsRefused(t *testing.T) {
	s := &RelayService{}
	_, err := s.rowFrom(RelayRequest{
		Name: "ir", Address: "185.0.0.1", Role: "front", Transport: "quic",
	}, nil)
	if err == nil {
		t.Fatal("an unknown transport was accepted")
	}
}
