package sub

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// seedCiscoSub puts one cisco inbound and one client on it into a fresh DB.
func seedCiscoSub(t *testing.T, email, subID, password string) {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	db := database.GetDB()

	ib := &model.Inbound{
		Id: 1, UserId: 1, Remark: "cisco-sub", Enable: true, Port: 4443,
		Protocol: model.Cisco,
		Settings: `{"clients":[{"email":"` + email + `","password":"` + password +
			`","subId":"` + subID + `","enable":true}]}`,
		StreamSettings: "{}", Sniffing: "{}",
	}
	if err := db.Create(ib).Error; err != nil {
		t.Fatalf("inbound: %v", err)
	}
	rec := &model.ClientRecord{Id: 1, Email: email, SubID: subID, Password: password, Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{InboundId: 1, ClientId: 1}).Error; err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := db.Create(&xray.ClientTraffic{InboundId: 1, Email: email, Enable: true}).Error; err != nil {
		t.Fatalf("traffic: %v", err)
	}
}

// The subscription query listed every protocol by hand and cisco was not in
// it, so a cisco-only subscription matched no inbound and getSubs returned
// nil — which the controller turns into a 404. That is the dead link a
// customer reports as "it does not open".
func TestCiscoSubscriptionIsNotA404(t *testing.T) {
	seedCiscoSub(t, "cisco-user", "sub-cisco", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	subs, _, _, _, err := s.getSubs("sub-cisco")
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if subs == nil {
		t.Fatal("getSubs returned nil for a cisco subscription; the controller answers that with 404")
	}
}

// AnyConnect has no URI format. An empty string appended to the link list
// would show up in every importing app as a blank entry, so cisco contributes
// no link at all — its credentials ride beside the list instead.
func TestCiscoContributesCredentialsNotAnEmptyLink(t *testing.T) {
	seedCiscoSub(t, "cisco-user", "sub-cisco", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	subs, _, _, _, err := s.getSubs("sub-cisco")
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	for _, link := range subs {
		if link == "" {
			t.Fatalf("an empty link was emitted; importing apps render that as a blank entry: %q", subs)
		}
	}
	if len(s.ciscoLogins) != 1 {
		t.Fatalf("got %d credential sets, want 1", len(s.ciscoLogins))
	}
	login := s.ciscoLogins[0]
	if login.Port != 4443 {
		t.Fatalf("port = %d, want the inbound's 4443", login.Port)
	}
	if login.Password != "pw123456" {
		t.Fatalf("password = %q, want the client's", login.Password)
	}
	if login.Server == "" {
		t.Fatal("no server address; the customer has nothing to connect to")
	}
}

// A reseller's client email is namespaced "a<adminId>:", and ocserv's password
// file is colon-separated, so the login name substitutes the colon. Handing
// the customer the email instead would hand them a login the server refuses.
func TestCiscoSubscriptionShowsTheLoginNotTheEmail(t *testing.T) {
	seedCiscoSub(t, "a9:customer@example.com", "sub-reseller", "pw123456")

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-reseller"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.ciscoLogins) != 1 {
		t.Fatalf("got %d credential sets, want 1", len(s.ciscoLogins))
	}
	login := s.ciscoLogins[0]
	if login.Username != "a9_customer@example.com" {
		t.Fatalf("username = %q, want the colon substituted", login.Username)
	}
	if login.Email != "a9:customer@example.com" {
		t.Fatalf("email = %q, want the billing identity unchanged", login.Email)
	}
}

// A client with no password cannot log in, so offering credentials for it
// would be telling the customer something untrue.
func TestCiscoClientWithoutAPasswordOffersNoCredentials(t *testing.T) {
	seedCiscoSub(t, "cisco-user", "sub-cisco", "")

	s := (&SubService{}).ForRequest("vpn.example.com")
	if _, _, _, _, err := s.getSubs("sub-cisco"); err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	if len(s.ciscoLogins) != 0 {
		t.Fatalf("credentials were offered for a client that cannot log in: %+v", s.ciscoLogins)
	}
}
