package sub

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// openvpnInbound describes one inbound to seed, so a test can ask for two of
// them without repeating the whole row.
type openvpnInbound struct {
	id       int
	remark   string
	port     int
	useTCP   bool
	protocol model.Protocol
}

// seedProtocolSub puts the given inbounds into a fresh DB with one client
// attached to every one of them, which is the shape of a customer who has
// bought more than one protocol.
func seedProtocolSub(t *testing.T, email, subID, password string, inbounds ...openvpnInbound) {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	db := database.GetDB()

	// The client row comes first: the attachment rows below point at it, and a
	// foreign key that is written before its target is a seed that passes on
	// one database engine and fails on the next.
	rec := &model.ClientRecord{Id: 1, Email: email, SubID: subID, Password: password, Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("client: %v", err)
	}

	for _, in := range inbounds {
		settings := fmt.Sprintf(
			`{"useTcp":%t,"clients":[{"email":%q,"password":%q,"subId":%q,"enable":true}]}`,
			in.useTCP, email, password, subID,
		)
		ib := &model.Inbound{
			Id: in.id, UserId: 1, Remark: in.remark, Enable: true, Port: in.port,
			// The tag is unique in the schema, so two seeded inbounds that
			// both leave it empty collide on the second insert.
			Tag:            fmt.Sprintf("inbound-%d", in.id),
			Protocol:       in.protocol,
			Settings:       settings,
			StreamSettings: "{}", Sniffing: "{}",
		}
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("inbound %d: %v", in.id, err)
		}
		if err := db.Create(&model.ClientInbound{InboundId: in.id, ClientId: 1}).Error; err != nil {
			t.Fatalf("attach %d: %v", in.id, err)
		}
	}

	// One traffic row, not one per inbound. The email is unique on that table
	// because usage is counted per customer and summed across whatever they
	// are attached to, so a row per inbound is not merely rejected by the
	// schema — it is the wrong shape.
	traffic := &xray.ClientTraffic{InboundId: inbounds[0].id, Email: email, Enable: true}
	if err := db.Create(traffic).Error; err != nil {
		t.Fatalf("traffic: %v", err)
	}
}

func seedOpenVPNSub(t *testing.T, email, subID, password string) {
	t.Helper()
	seedProtocolSub(t, email, subID, password,
		openvpnInbound{id: 1, remark: "Germany", port: 1194, protocol: model.OpenVPN})
}

// The subscription query lists every protocol by hand. Cisco was once missing
// from it and a cisco-only subscription returned nil, which the controller
// answers with 404 — the dead link a customer reports as "it does not open".
// OpenVPN joins the same list and has the same way of going wrong.
func TestOpenVPNSubscriptionIsNotA404(t *testing.T) {
	seedOpenVPNSub(t, "ovpn-user", "sub-ovpn", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	subs, _, _, _, err := s.getSubs("sub-ovpn")
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if subs == nil {
		t.Fatal("getSubs returned nil for an OpenVPN subscription; the controller answers that with 404")
	}
}

// OpenVPN has no URI format either. An empty string in the link list shows up
// in every importing app as a blank entry, so the inbound contributes a
// downloadable profile beside the list instead of a link inside it.
func TestOpenVPNContributesAProfileNotAnEmptyLink(t *testing.T) {
	seedOpenVPNSub(t, "ovpn-user", "sub-ovpn", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	subs, _, _, _, err := s.getSubs("sub-ovpn")
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	for _, link := range subs {
		if link == "" {
			t.Fatalf("an empty link was emitted; importing apps render that as a blank entry: %q", subs)
		}
	}
	if len(s.openvpnProfiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(s.openvpnProfiles))
	}
	p := s.openvpnProfiles[0]
	if p.Port != 1194 {
		t.Errorf("port = %d, want the inbound's 1194", p.Port)
	}
	if p.Password != "pw123456" {
		t.Errorf("password = %q, want the client's", p.Password)
	}
	if p.Address == "" {
		t.Error("no address; the rendered file would have nothing to connect to")
	}
	if p.Label != "Germany" {
		t.Errorf("label = %q, want the inbound's remark — that is the country name the customer sees in their client", p.Label)
	}
	if p.FileName != "Germany.ovpn" {
		t.Errorf("file name = %q, want it named after the country", p.FileName)
	}
}

// A customer who bought both protocols has to come away with both. Cisco
// credentials and an OpenVPN file are collected in the same pass, and one
// protocol taking the "continue" branch must not skip the other.
func TestACustomerWithBothProtocolsGetsBoth(t *testing.T) {
	seedProtocolSub(t, "both-user", "sub-both", "pw123456",
		openvpnInbound{id: 1, remark: "Germany", port: 4443, protocol: model.Cisco},
		openvpnInbound{id: 2, remark: "Netherlands", port: 1194, protocol: model.OpenVPN},
	)

	s := (&SubService{}).ForRequest("vpn.example.com")
	subs, _, _, _, err := s.getSubs("sub-both")
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if subs == nil {
		t.Fatal("getSubs returned nil; the controller answers that with 404")
	}
	if len(s.ciscoLogins) != 1 {
		t.Errorf("got %d cisco logins, want 1", len(s.ciscoLogins))
	}
	if len(s.openvpnProfiles) != 1 {
		t.Errorf("got %d openvpn profiles, want 1", len(s.openvpnProfiles))
	}
}

// The username is not the email. A reseller's clients are namespaced
// "a<adminId>:" and the shared password file is colon-separated, so the colon
// is substituted on the way in; printing the email would hand the customer a
// login the server refuses.
func TestOpenVPNProfileShowsTheLoginNotTheEmail(t *testing.T) {
	seedOpenVPNSub(t, "a9:customer@example.com", "sub-reseller", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-reseller"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(s.openvpnProfiles))
	}
	p := s.openvpnProfiles[0]
	if p.Username != "a9_customer@example.com" {
		t.Errorf("username = %q, want the colon substituted", p.Username)
	}
	if p.Email != "a9:customer@example.com" {
		t.Errorf("email = %q, want the billing identity unchanged", p.Email)
	}
}

// A client with no password cannot log in, so offering it a file would be
// telling the customer something untrue.
func TestOpenVPNClientWithoutAPasswordOffersNoProfile(t *testing.T) {
	seedOpenVPNSub(t, "ovpn-user", "sub-ovpn", "")

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-ovpn"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 0 {
		t.Fatalf("a profile was offered to a client that cannot log in: %+v", s.openvpnProfiles)
	}
}

// Two files arriving under one name is a customer with two countries who
// cannot tell which is which. The clean name goes to the first; the second
// carries its inbound id.
func TestTwoInboundsNamedTheSameGetDistinctFileNames(t *testing.T) {
	seedProtocolSub(t, "ovpn-user", "sub-ovpn", "pw123456",
		openvpnInbound{id: 1, remark: "Germany", port: 1194, protocol: model.OpenVPN},
		openvpnInbound{id: 2, remark: "Germany", port: 1195, protocol: model.OpenVPN},
	)

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-ovpn"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(s.openvpnProfiles))
	}
	first, second := s.openvpnProfiles[0].FileName, s.openvpnProfiles[1].FileName
	if first == second {
		t.Fatalf("both files are called %q; the customer cannot tell the two countries apart", first)
	}
	if first != "Germany.ovpn" {
		t.Errorf("first file = %q, want the clean name", first)
	}
	if second != "Germany-2.ovpn" {
		t.Errorf("second file = %q, want the inbound id to separate it", second)
	}
}

// An operator who names their inbounds in a script other than Latin gets
// nothing back from the name cleaner, so every file would be "vpn.ovpn".
func TestInboundsNamedInAnotherScriptStillGetSeparateFiles(t *testing.T) {
	seedProtocolSub(t, "ovpn-user", "sub-ovpn", "pw123456",
		openvpnInbound{id: 1, remark: "آلمان", port: 1194, protocol: model.OpenVPN},
		openvpnInbound{id: 2, remark: "هلند", port: 1195, protocol: model.OpenVPN},
	)

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-ovpn"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(s.openvpnProfiles))
	}
	if s.openvpnProfiles[0].FileName == s.openvpnProfiles[1].FileName {
		t.Fatalf("both files are called %q", s.openvpnProfiles[0].FileName)
	}
}

// The transport in the customer's file has to match the one the server
// listens on. A profile that says udp against a tcp server does not fail with
// a wrong-protocol error; it waits until it times out.
func TestTheProfileCarriesTheInboundsTransport(t *testing.T) {
	seedProtocolSub(t, "ovpn-user", "sub-ovpn", "pw123456",
		openvpnInbound{id: 1, remark: "Germany", port: 1194, protocol: model.OpenVPN},
		openvpnInbound{id: 2, remark: "Netherlands", port: 443, useTCP: true, protocol: model.OpenVPN},
	)

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-ovpn"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(s.openvpnProfiles))
	}
	byID := map[int]OpenVPNProfile{}
	for _, p := range s.openvpnProfiles {
		byID[p.InboundId] = p
	}
	if byID[1].UseTCP {
		t.Error("the udp inbound's profile says tcp")
	}
	if !byID[2].UseTCP {
		t.Error("the tcp inbound's profile says udp, and the client would sit there until it timed out")
	}
}

// Per-request state. A profile left over from the previous subscription would
// hand one customer another customer's file and password.
func TestProfilesDoNotSurviveIntoTheNextRequest(t *testing.T) {
	seedOpenVPNSub(t, "ovpn-user", "sub-ovpn", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-ovpn"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(s.openvpnProfiles))
	}
	if _, _, _, _, err := s.getSubs("sub-nobody"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.openvpnProfiles) != 0 {
		t.Fatalf("the previous customer's profile is still here: %+v", s.openvpnProfiles)
	}
}
