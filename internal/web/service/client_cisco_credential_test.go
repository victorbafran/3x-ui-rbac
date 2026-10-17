package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedCiscoInbound(t *testing.T, port int) *model.Inbound {
	t.Helper()
	ib := &model.Inbound{
		Tag: "cisco-cred", Remark: "cisco-cred", Enable: true, Port: port,
		Protocol: model.Cisco, Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}",
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	return ib
}

// A cisco client has no UUID. Its whole credential is the name it types into
// the AnyConnect client plus a password, so a client saved without one would
// be written into ocserv's password file as an account nobody can log into —
// and the panel would report success. Measured on the running panel before
// this guard existed: the add was refused with "empty client ID", a message
// about a field cisco does not have.
func TestAddInboundClientRefusesACiscoClientWithNoPassword(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	ib := seedCiscoInbound(t, 44301)

	svc := &ClientService{}
	inboundSvc := &InboundService{}
	_, err := svc.AddInboundClient(inboundSvc, &model.Inbound{
		Id: ib.Id, Protocol: model.Cisco,
		Settings: `{"clients":[{"email":"nopass@example.com","enable":true}]}`,
	})
	if err == nil {
		t.Fatal("a cisco client with no password was accepted")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("error %q does not say the password is what is missing", err)
	}
}

// The same path has to accept the credential cisco actually uses.
func TestAddInboundClientAcceptsACiscoClientWithAPassword(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	ib := seedCiscoInbound(t, 44302)

	svc := &ClientService{}
	inboundSvc := &InboundService{}
	if _, err := svc.AddInboundClient(inboundSvc, &model.Inbound{
		Id: ib.Id, Protocol: model.Cisco,
		Settings: `{"clients":[{"email":"withpass@example.com","password":"s3cret","enable":true}]}`,
	}); err != nil {
		t.Fatalf("a cisco client with a password was refused: %v", err)
	}
	if emails := settingsClientEmails(t, ib.Id); len(emails) != 1 || emails[0] != "withpass@example.com" {
		t.Fatalf("settings hold %v, want the one client", emails)
	}
}

// A name ocserv's password file cannot hold must never be saved: the file is
// colon-separated and line-based, so such a name would let one client's entry
// rewrite another's.
//
// Today the general email validation refuses it before the cisco branch is
// reached — removing the branch's own ValidLogin check leaves this test green.
// The check stays as the last thing standing between a stored name and the
// file, and this test guards the property rather than the layer: if the
// upstream validation is ever loosened, this is what notices.
func TestAddInboundClientRefusesACiscoNameThePasswordFileCannotHold(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	ib := seedCiscoInbound(t, 44303)

	svc := &ClientService{}
	inboundSvc := &InboundService{}
	_, err := svc.AddInboundClient(inboundSvc, &model.Inbound{
		Id: ib.Id, Protocol: model.Cisco,
		Settings: `{"clients":[{"email":"bad\nname@example.com","password":"s3cret","enable":true}]}`,
	})
	if err == nil {
		t.Fatal("a client name carrying a line break was accepted")
	}
}
