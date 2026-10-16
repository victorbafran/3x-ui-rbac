package panel

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

func newAdminService(t *testing.T) *AdminService {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	return &AdminService{}
}

func firstAdminId(t *testing.T) int {
	t.Helper()
	var u model.User
	if err := database.GetDB().First(&u).Error; err != nil {
		t.Fatalf("first user: %v", err)
	}
	return u.Id
}

// A listing that carries hashes puts every admin's bcrypt digest in the
// browser of anyone who may open the page.
func TestListNeverReturnsPasswordHashes(t *testing.T) {
	s := newAdminService(t)
	if _, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	admins, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(admins) != 2 {
		t.Fatalf("got %d admins, want 2", len(admins))
	}
	for _, a := range admins {
		if a.Password != "" {
			t.Fatalf("%s carries a password hash", a.Username)
		}
	}
}

func TestCreateStoresHashedPasswordAndRole(t *testing.T) {
	s := newAdminService(t)
	created, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 500)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var stored model.User
	if err := database.GetDB().Where("id = ?", created.Id).First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Password == "pw-agent-123" {
		t.Fatal("password stored in clear")
	}
	if !crypto.CheckPasswordHash(stored.Password, "pw-agent-123") {
		t.Fatal("stored hash does not verify the password")
	}
	if stored.Role != model.RoleReseller || !stored.Enabled || stored.TrafficQuota != 500 {
		t.Fatalf("stored = %+v, want reseller/enabled/quota 500", stored)
	}
}

func TestCreateRejectsDuplicateUsername(t *testing.T) {
	s := newAdminService(t)
	if _, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 0); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.Create("agent", "other-pw-456", model.RoleManager, 0); err == nil {
		t.Fatal("duplicate username was accepted")
	}
}

func TestCreateRejectsUnknownRole(t *testing.T) {
	s := newAdminService(t)
	if _, err := s.Create("agent", "pw-agent-123", model.Role("root"), 0); err == nil {
		t.Fatal("unknown role was accepted")
	}
}

// Deleting yourself locks you out mid-session for no good reason.
func TestDeleteRefusesSelf(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if err := s.Delete(me, me); err == nil {
		t.Fatal("deleting yourself was accepted")
	}
}

// Removing the last super_admin leaves a panel nobody can fully administer.
func TestDeleteRefusesLastSuperAdmin(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	other, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// other deletes me: the target is the only super_admin left.
	if err := s.Delete(other.Id, me); err == nil {
		t.Fatal("deleting the last super_admin was accepted")
	}
	// Deleting the reseller instead is ordinary housekeeping and must work.
	if err := s.Delete(me, other.Id); err != nil {
		t.Fatalf("deleting a reseller: %v", err)
	}
}

// Demotion is the same hole by another route.
func TestUpdateRefusesDemotingLastSuperAdmin(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if err := s.Update(me, model.RoleManager, true, 0); err == nil {
		t.Fatal("demoting the last super_admin was accepted")
	}
}

// Disabling the last super_admin locks the panel just as effectively.
func TestUpdateRefusesDisablingLastSuperAdmin(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if err := s.Update(me, model.RoleSuperAdmin, false, 0); err == nil {
		t.Fatal("disabling the last super_admin was accepted")
	}
}

// With a second super_admin present, both moves are legitimate.
func TestUpdateAllowsDemotionWhenAnotherSuperAdminRemains(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if _, err := s.Create("backup", "pw-backup-123", model.RoleSuperAdmin, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Update(me, model.RoleManager, true, 0); err != nil {
		t.Fatalf("demote with a spare super_admin: %v", err)
	}
}

// A reset must end the target's open sessions, or a dismissed reseller keeps
// working from the browser tab they already have open.
func TestResetPasswordBumpsLoginEpoch(t *testing.T) {
	s := newAdminService(t)
	created, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var before model.User
	if err := database.GetDB().Where("id = ?", created.Id).First(&before).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := s.ResetPassword(created.Id, "fresh-pw-789"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var after model.User
	if err := database.GetDB().Where("id = ?", created.Id).First(&after).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.LoginEpoch <= before.LoginEpoch {
		t.Fatalf("login epoch %d did not advance past %d", after.LoginEpoch, before.LoginEpoch)
	}
	if !crypto.CheckPasswordHash(after.Password, "fresh-pw-789") {
		t.Fatal("new password does not verify")
	}
}

// Disabling an admin must also end their sessions; the authz gate refuses a
// disabled user, but only on their next request through it.
func TestUpdateDisablingBumpsLoginEpoch(t *testing.T) {
	s := newAdminService(t)
	created, err := s.Create("agent", "pw-agent-123", model.RoleReseller, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Update(created.Id, model.RoleReseller, false, 0); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var after model.User
	if err := database.GetDB().Where("id = ?", created.Id).First(&after).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.LoginEpoch == 0 {
		t.Fatal("disabling left the login epoch untouched")
	}
}
