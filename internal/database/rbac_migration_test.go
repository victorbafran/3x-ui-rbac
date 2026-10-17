package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Pre-RBAC users schema: every row in it was a full admin.
const legacyUsersNoRoleDDL = "CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`username` text,`password` text,`login_epoch` integer DEFAULT 0)"

func openLegacyUsersDB(t *testing.T, dbPath string, usernames ...string) {
	t.Helper()
	legacy, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if err := legacy.Exec(legacyUsersNoRoleDDL).Error; err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	for _, name := range usernames {
		if err := legacy.Exec("INSERT INTO users (username, password) VALUES (?, ?)", name, "hash").Error; err != nil {
			t.Fatalf("seed legacy user %s: %v", name, err)
		}
	}
	sqlDB, err := legacy.DB()
	if err != nil {
		t.Fatalf("legacy db handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
}

func loadUser(t *testing.T, username string) model.User {
	t.Helper()
	var user model.User
	if err := GetDB().Where("username = ?", username).First(&user).Error; err != nil {
		t.Fatalf("load user %s: %v", username, err)
	}
	return user
}

// An admin that predates the role column must stay a full admin. Without the
// backfill the column default locks them out of their own panel as readonly.
func TestSuperAdminBackfillPromotesPreRBACAdmins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	openLegacyUsersDB(t, dbPath, "admin", "second")

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB over legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	for _, name := range []string{"admin", "second"} {
		user := loadUser(t, name)
		if user.Role != model.RoleSuperAdmin {
			t.Fatalf("%s role = %q, want %q", name, user.Role, model.RoleSuperAdmin)
		}
		if !user.Enabled {
			t.Fatalf("%s must stay enabled after migration", name)
		}
	}
}

// The backfill is a one-time upgrade step. If it ran on every boot it would
// silently re-promote a reseller that a super_admin had demoted.
func TestSuperAdminBackfillRunsOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	openLegacyUsersDB(t, dbPath, "admin", "agent")

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB over legacy schema: %v", err)
	}
	if err := GetDB().Model(&model.User{}).Where("username = ?", "agent").Updates(map[string]any{
		"role":    string(model.RoleReseller),
		"enabled": false,
	}).Error; err != nil {
		t.Fatalf("demote agent: %v", err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("reopen migrated db: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	agent := loadUser(t, "agent")
	if agent.Role != model.RoleReseller {
		t.Fatalf("agent role = %q after reboot, want %q", agent.Role, model.RoleReseller)
	}
	if agent.Enabled {
		t.Fatal("agent must stay disabled after reboot")
	}
}

// A brand-new install has to end up with a usable super_admin, not the
// readonly the column default hands out.
func TestFreshInstallSeedsSuperAdmin(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB on empty path: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	admin := loadUser(t, defaultUsername)
	if admin.Role != model.RoleSuperAdmin {
		t.Fatalf("default admin role = %q, want %q", admin.Role, model.RoleSuperAdmin)
	}
}

// The relay table must never gain a password column: the enrollment password
// is used once and dropped, so a DB dump cannot carry relay root access.
func TestRelayTableStoresKeyPathNotPassword(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB on empty path: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	migrator := GetDB().Migrator()
	if !migrator.HasTable(&model.Relay{}) {
		t.Fatal("relays table missing; the first migration must create it")
	}
	if !migrator.HasColumn(&model.Relay{}, "key_path") {
		t.Fatal("relays.key_path missing")
	}
	for _, col := range []string{"password", "ssh_password"} {
		if migrator.HasColumn(&model.Relay{}, col) {
			t.Fatalf("relays.%s exists; SSH passwords must never be stored", col)
		}
	}
}
