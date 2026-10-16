package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedOwnedHost(t *testing.T, username, tag, remark string) int {
	t.Helper()
	db := database.GetDB()
	u := model.User{Username: username, Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ib := model.Inbound{UserId: u.Id, Tag: tag, Remark: tag, Protocol: model.VLESS,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	h := model.Host{GroupId: tag + "-grp", InboundId: ib.Id, Remark: remark, Address: remark + ".example.com"}
	if err := db.Create(&h).Error; err != nil {
		t.Fatalf("create host: %v", err)
	}
	return u.Id
}

// Host rows name the domains and SNI an admin fronts their inbounds with.
// A reseller listing hosts must see only the ones on inbounds they own.
func TestGetHostsForOwnerHidesOtherAdminsHosts(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	mineId := seedOwnedHost(t, "host-owner-a", "htag-a", "mine")
	seedOwnedHost(t, "host-owner-b", "htag-b", "theirs")

	svc := &HostService{}
	groups, err := svc.GetHostsForOwner(mineId)
	if err != nil {
		t.Fatalf("GetHostsForOwner: %v", err)
	}
	for _, g := range groups {
		if g.Remark != "mine" {
			t.Fatalf("leaked host group %q belonging to another admin", g.Remark)
		}
	}
	if len(groups) != 1 {
		t.Fatalf("got %d host groups, want 1", len(groups))
	}
}

// Owner 0 is the super_admin path and must still see every host.
func TestGetHostsForOwnerUnscopedSeesAll(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	seedOwnedHost(t, "host-owner-a", "htag-a", "mine")
	seedOwnedHost(t, "host-owner-b", "htag-b", "theirs")

	groups, err := (&HostService{}).GetHostsForOwner(0)
	if err != nil {
		t.Fatalf("GetHostsForOwner(0): %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d host groups, want 2", len(groups))
	}
}
