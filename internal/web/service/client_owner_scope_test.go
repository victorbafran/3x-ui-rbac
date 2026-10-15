package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// seedOwnedClient creates an admin, an inbound they own, and a client on it.
func seedOwnedClient(t *testing.T, username, tag, email string) int {
	t.Helper()
	db := database.GetDB()
	u := model.User{Username: username, Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	ib := model.Inbound{UserId: u.Id, Tag: tag, Remark: tag, Protocol: model.VLESS,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	cr := model.ClientRecord{Email: email, Enable: true}
	if err := db.Create(&cr).Error; err != nil {
		t.Fatalf("create client %s: %v", email, err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: cr.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatalf("attach client %s: %v", email, err)
	}
	return u.Id
}

// A reseller listing clients must see only the ones on inbounds they own.
// Unfiltered, List returns every customer on the panel, emails and UUIDs included.
func TestListForOwnerHidesOtherAdminsClients(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	mineId := seedOwnedClient(t, "owner-a", "tag-a", "a@example.com")
	seedOwnedClient(t, "owner-b", "tag-b", "b@example.com")

	svc := &ClientService{}
	rows, err := svc.ListForOwner(mineId)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d clients, want 1", len(rows))
	}
	if rows[0].Email != "a@example.com" {
		t.Fatalf("got %q, want a@example.com", rows[0].Email)
	}
}

// The paged list is what the panel's client page actually calls, and it counts
// as well as lists: a leaking total tells a reseller how many customers exist.
func TestListPagedScopesToOwner(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	mineId := seedOwnedClient(t, "owner-a", "tag-a", "a@example.com")
	seedOwnedClient(t, "owner-b", "tag-b", "b@example.com")

	svc := &ClientService{}
	ib := &InboundService{}
	set := &SettingService{}
	resp, err := svc.ListPaged(ib, set, ClientPageParams{OwnerAdminId: mineId})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if len(resp.Items) != 1 || resp.Items[0].Email != "a@example.com" {
		t.Fatalf("items = %+v, want only a@example.com", resp.Items)
	}
	if resp.Summary.Total != 1 {
		t.Fatalf("summary total = %d, want 1", resp.Summary.Total)
	}
}

// Owner 0 means "no scoping": the super_admin path must still see everyone.
func TestListPagedWithoutOwnerSeesAll(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	seedOwnedClient(t, "owner-a", "tag-a", "a@example.com")
	seedOwnedClient(t, "owner-b", "tag-b", "b@example.com")

	svc := &ClientService{}
	resp, err := svc.ListPaged(&InboundService{}, &SettingService{}, ClientPageParams{})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
}
