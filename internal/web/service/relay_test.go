package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
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

// An edited public name was dropped: the edit wrote every other field but
// that one, so the inbound form went on offering the old name.
func TestAnEditedPublicNameIsSaved(t *testing.T) {
	s := &RelayService{}
	base, err := s.rowFrom(RelayRequest{Name: "fr", Address: "198.51.100.5", Role: "front", PublicName: "old.example.ir"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := s.rowFrom(RelayRequest{Name: "fr", Address: "198.51.100.5", Role: "front", PublicName: "New.Example.ir."}, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := editableFields(edited)["public_name"]; got != "new.example.ir" {
		t.Errorf("the edit writes public_name %v, want new.example.ir", got)
	}
	exit, err := s.rowFrom(RelayRequest{Name: "nl", Address: "198.51.100.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := editableFields(exit)["public_name"]; ok {
		t.Error("an exit, which has no public name, had one written")
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

// udp as a front's main tunnel cuts every TCP inbound off, so a new choice of
// it is refused; a front saved on it before must still take other edits.
func TestUDPCannotBeChosenButAStoredUDPFrontKeepsSaving(t *testing.T) {
	s := &RelayService{}
	for _, tool := range []string{"backhaul", "backpack"} {
		_, err := s.rowFrom(RelayRequest{Name: "ir", Address: "185.0.0.1", Role: "front", Tool: tool, Transport: "udp"}, nil)
		if err == nil || !strings.Contains(err.Error(), "carries only UDP") {
			t.Errorf("%s: a new udp front was not refused for the reason: %v", tool, err)
		}
	}

	base, err := s.rowFrom(RelayRequest{Name: "ir", Address: "185.0.0.1", Role: "front"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rowFrom(RelayRequest{Name: "ir", Address: "185.0.0.1", Role: "front", Transport: "udp"}, base); err == nil {
		t.Error("a tcp front was moved onto udp")
	}

	base.Transport = "udp"
	edited, err := s.rowFrom(RelayRequest{Name: "ir", Address: "185.0.0.2", Role: "front", Transport: "udp"}, base)
	if err != nil {
		t.Fatalf("a front stored on udp can no longer be edited: %v", err)
	}
	if edited.Transport != "udp" {
		t.Errorf("transport = %q, want the stored udp", edited.Transport)
	}
}

// The minute reconcile and a save's own provisioning on one front interleave
// their steps on it; two runs once left a key from one and a certificate from
// the other. The reconcile stands aside while the save's run is going.
func TestTheReconcileStandsAsideWhileASaveProvisionsTheFront(t *testing.T) {
	setupRelayDB(t)
	// A key that is not there stops the reconcile before any SSH, with an
	// error the row shows, which is how the test sees whether it ran.
	seedRelays(t, model.Relay{
		Name: "iran-front", Address: "203.0.113.9", Role: model.RelayFront, Tunnel: "backhaul", Enabled: true,
		KeyPath: filepath.Join(t.TempDir(), "gone"), TunnelToken: "a-token", Status: RelayWorking,
	})
	var r model.Relay
	if err := database.GetDB().First(&r).Error; err != nil {
		t.Fatal(err)
	}
	status := func() string {
		t.Helper()
		var got model.Relay
		if err := database.GetDB().First(&got, r.Id).Error; err != nil {
			t.Fatal(err)
		}
		return got.Status
	}

	relayRunning.Store(r.Id, true)
	t.Cleanup(func() { relayRunning.Delete(r.Id) })
	(&RelayService{}).reconcileFront(r)
	if got := status(); got != RelayWorking {
		t.Errorf("the reconcile ran beside a save's provisioning: status %q", got)
	}

	relayRunning.Delete(r.Id)
	(&RelayService{}).reconcileFront(r)
	if got := status(); got != RelayError {
		t.Errorf("with nothing in flight the reconcile did not run: status %q", got)
	}
}
