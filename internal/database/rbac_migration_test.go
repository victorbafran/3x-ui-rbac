package database

import (
	"bytes"
	"encoding/json"
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

	// The first user now arrives after InitDB, once its backfill has already run.
	username, _, err := EnsureFirstUser()
	if err != nil {
		t.Fatalf("EnsureFirstUser: %v", err)
	}
	admin := loadUser(t, username)
	if admin.Role != model.RoleSuperAdmin || !admin.Enabled {
		t.Fatalf("first user role = %q enabled = %v, want an enabled %q", admin.Role, admin.Enabled, model.RoleSuperAdmin)
	}
}

// The relay table must never gain a password column: the enrollment password
// is used once and dropped, so a DB dump cannot carry relay root access.
// A relay enrolled before fronts existed is an exit, and the migration has to
// say so. Reading those rows back as anything else would point the panel's
// customers at a machine that was never meant to accept them, or stop sending
// traffic out through one that was.
func TestRelaysEnrolledBeforeFrontsExistedAreStillExits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	db := GetDB()
	// Written the way a pre-front panel wrote them: nothing in the new
	// columns at all.
	if err := db.Exec(
		"INSERT INTO relays (id, name, address, ssh_port, ssh_user, key_path, tunnel) VALUES (1, ?, ?, 22, 'root', ?, 'wireguard')",
		"relay-old", "198.51.100.4", "/etc/x-ui/relay-keys/relay-old",
	).Error; err != nil {
		t.Fatalf("insert an old-style row: %v", err)
	}

	var got model.Relay
	if err := db.First(&got, 1).Error; err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Role != model.RelayExit {
		t.Fatalf("role read back as %q; a relay that predates fronts is an exit", got.Role)
	}
	if got.TunnelPort != 0 || got.TunnelToken != "" {
		t.Errorf("a front's fields came back filled on an exit: port %d token %q", got.TunnelPort, got.TunnelToken)
	}
}

// The role decides which machine the panel dials and which it sends customers
// to, so a typo must not be stored as a third kind of relay nobody handles.
func TestOnlyTheTwoRolesAreValid(t *testing.T) {
	for _, good := range []model.RelayRole{model.RelayExit, model.RelayFront} {
		if !good.Valid() {
			t.Errorf("%q was refused", good)
		}
	}
	for _, bad := range []model.RelayRole{"", "exits", "FRONT", "relay", "both"} {
		if bad.Valid() {
			t.Errorf("%q was accepted as a role", bad)
		}
	}
}

// The token joins the tunnel. A column that gave it back to anything reading
// the table is one leak away from a stranger inside the tunnel, so it is
// never serialised outward.
func TestTheTunnelTokenIsNeverSerialised(t *testing.T) {
	body, err := json.Marshal(model.Relay{Id: 1, Name: "front-1", TunnelToken: "s3cret-token"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(body, []byte("s3cret-token")) {
		t.Fatalf("the tunnel token is in the json the panel hands out: %s", body)
	}
}

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
