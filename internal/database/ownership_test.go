package database

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedAdmin(t *testing.T, username string) int {
	t.Helper()
	u := model.User{Username: username, Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := GetDB().Create(&u).Error; err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return u.Id
}

func seedInboundFor(t *testing.T, ownerId int, tag string) int {
	t.Helper()
	ib := model.Inbound{UserId: ownerId, Tag: tag, Remark: tag, Protocol: model.VLESS,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := GetDB().Create(&ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	return ib.Id
}

// An admin owns what they created plus what a super_admin handed them, and
// nothing else. This one condition backs every list, lookup and gate.
func TestOwnedInboundsCoversCreatedAndAssigned(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	a := seedAdmin(t, "owner-a")
	b := seedAdmin(t, "owner-b")
	seedInboundFor(t, a, "a-created")
	seedInboundFor(t, b, "b-private")
	assigned := seedInboundFor(t, b, "b-assigned-to-a")
	if err := GetDB().Create(&model.AdminInbound{AdminId: a, InboundId: assigned}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}

	var tags []string
	if err := GetDB().Model(&model.Inbound{}).Scopes(OwnedInbounds(a)).Pluck("tag", &tags).Error; err != nil {
		t.Fatalf("scoped pluck: %v", err)
	}
	sort.Strings(tags)
	want := []string{"a-created", "b-assigned-to-a"}
	if len(tags) != 2 || tags[0] != want[0] || tags[1] != want[1] {
		t.Fatalf("owner a sees %v, want %v", tags, want)
	}

	var all int64
	if err := GetDB().Model(&model.Inbound{}).Scopes(OwnedInbounds(0)).Count(&all).Error; err != nil {
		t.Fatalf("unscoped count: %v", err)
	}
	if all != 3 {
		t.Fatalf("owner 0 (super_admin) sees %d inbounds, want 3", all)
	}
}
