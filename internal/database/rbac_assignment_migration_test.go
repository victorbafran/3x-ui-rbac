package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Upgrading from the first RBAC schema (role/enabled/quota, no switches) must
// add the switch and limit columns and the assignment table in one boot, and
// leave existing admins on their role template (empty overrides).
func TestMigrationAddsPermissionColumnsAndAssignments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	openLegacyUsersDB(t, dbPath, "admin")

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	m := GetDB().Migrator()
	for _, col := range []string{"permissions", "users_limit", "expire_at"} {
		if !m.HasColumn(&model.User{}, col) {
			t.Errorf("users.%s missing after migration", col)
		}
	}
	if !m.HasTable(&model.AdminInbound{}) {
		t.Fatal("admin_inbounds table missing; the assignment phase needs it")
	}
	admin := loadUser(t, "admin")
	if admin.Permissions != "" {
		t.Fatalf("pre-existing admin got overrides %q; want the bare role template", admin.Permissions)
	}
	if admin.Role != model.RoleSuperAdmin {
		t.Fatalf("pre-existing admin role = %q, want super_admin", admin.Role)
	}
}

// The join table must reject the same inbound being handed to the same admin
// twice; otherwise a repeated save in the UI silently duplicates rows.
func TestAdminInboundAssignmentIsUnique(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	row := model.AdminInbound{AdminId: 2, InboundId: 7}
	if err := GetDB().Create(&row).Error; err != nil {
		t.Fatalf("first assignment: %v", err)
	}
	if err := GetDB().Create(&model.AdminInbound{AdminId: 2, InboundId: 7}).Error; err == nil {
		t.Fatal("duplicate assignment was accepted")
	}
}
