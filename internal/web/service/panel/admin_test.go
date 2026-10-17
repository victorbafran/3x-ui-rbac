package panel

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
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

func reseller(username, password string) AdminSpec {
	return AdminSpec{Username: username, Password: password, Role: model.RoleReseller, Enabled: true}
}

func reload(t *testing.T, id int) model.User {
	t.Helper()
	var u model.User
	if err := database.GetDB().Where("id = ?", id).First(&u).Error; err != nil {
		t.Fatalf("reload %d: %v", id, err)
	}
	return u
}

func seedInbound(t *testing.T, ownerId int, tag string) int {
	t.Helper()
	ib := model.Inbound{
		UserId: ownerId, Tag: tag, Remark: tag, Protocol: model.VLESS,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}",
	}
	if err := database.GetDB().Create(&ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	return ib.Id
}

// The view type has no password field at all; this pins that the service
// never builds its output from the raw row by accident.
func TestListReturnsViewsWithoutHashes(t *testing.T) {
	s := newAdminService(t)
	if _, err := s.Create(reseller("agent", "pw-agent-123")); err != nil {
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
		if a.Username == "" || !model.Role(a.Role).Valid() {
			t.Fatalf("malformed view %+v", a)
		}
	}
}

func TestCreateStoresHashedPasswordRoleLimitsAndOverrides(t *testing.T) {
	s := newAdminService(t)
	spec := reseller("agent", "pw-agent-123")
	spec.TrafficQuota, spec.UsersLimit, spec.ExpireAt = 500, 20, 1_800_000_000
	spec.Permissions = map[string]bool{authz.ClientDelete: true, "bogus.key": true}
	created, err := s.Create(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := reload(t, created.Id)
	if stored.Password == "pw-agent-123" {
		t.Fatal("password stored in clear")
	}
	if !crypto.CheckPasswordHash(stored.Password, "pw-agent-123") {
		t.Fatal("stored hash does not verify the password")
	}
	if stored.Role != model.RoleReseller || !stored.Enabled {
		t.Fatalf("stored role/enabled = %s/%v, want reseller/true", stored.Role, stored.Enabled)
	}
	if stored.TrafficQuota != 500 || stored.UsersLimit != 20 || stored.ExpireAt != 1_800_000_000 {
		t.Fatalf("limits stored as %d/%d/%d", stored.TrafficQuota, stored.UsersLimit, stored.ExpireAt)
	}
	if stored.Permissions != `{"clients.delete":true}` {
		t.Fatalf("overrides stored as %q; the unknown key must be dropped", stored.Permissions)
	}
	if !created.Permissions[authz.ClientDelete] || len(created.Permissions) != 1 {
		t.Fatalf("view overrides = %v, want only clients.delete", created.Permissions)
	}
}

func TestCreateRejectsDuplicateUsername(t *testing.T) {
	s := newAdminService(t)
	if _, err := s.Create(reseller("agent", "pw-agent-123")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.Create(reseller("agent", "other-pw-456")); err == nil {
		t.Fatal("duplicate username was accepted")
	}
}

func TestCreateRejectsUnknownRoleAndShortPassword(t *testing.T) {
	s := newAdminService(t)
	bad := reseller("agent", "pw-agent-123")
	bad.Role = model.Role("root")
	if _, err := s.Create(bad); err == nil {
		t.Fatal("unknown role was accepted")
	}
	if _, err := s.Create(reseller("agent", "short")); err == nil {
		t.Fatal("7-character password was accepted")
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
func TestDeleteRefusesLastSuperAdminButRemovesResellerWithAssignments(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	other, err := s.Create(reseller("agent", "pw-agent-123"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ib := seedInbound(t, me, "handed")
	if err := s.SetAssignments(other.Id, []int{ib}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := s.Delete(other.Id, me); err == nil {
		t.Fatal("deleting the last super_admin was accepted")
	}
	if err := s.Delete(me, other.Id); err != nil {
		t.Fatalf("deleting a reseller: %v", err)
	}
	var dangling int64
	if err := database.GetDB().Model(&model.AdminInbound{}).Where("admin_id = ?", other.Id).Count(&dangling).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if dangling != 0 {
		t.Fatalf("%d assignment rows survived the admin's deletion", dangling)
	}
}

// Demotion and disabling are the same hole by another route.
func TestUpdateRefusesStrippingTheLastSuperAdmin(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if err := s.Update(me, AdminSpec{Role: model.RoleManager, Enabled: true}); err == nil {
		t.Fatal("demoting the last super_admin was accepted")
	}
	if err := s.Update(me, AdminSpec{Role: model.RoleSuperAdmin, Enabled: false}); err == nil {
		t.Fatal("disabling the last super_admin was accepted")
	}
}

// With a second super_admin present, demotion is legitimate.
func TestUpdateAllowsDemotionWhenAnotherSuperAdminRemains(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	backup := reseller("backup", "pw-backup-123")
	backup.Role = model.RoleSuperAdmin
	if _, err := s.Create(backup); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Update(me, AdminSpec{Role: model.RoleManager, Enabled: true}); err != nil {
		t.Fatalf("demote with a spare super_admin: %v", err)
	}
}

// Overrides and limits are editable after the fact; the view reads them back.
func TestUpdateRewritesOverridesAndLimits(t *testing.T) {
	s := newAdminService(t)
	created, err := s.Create(reseller("agent", "pw-agent-123"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = s.Update(created.Id, AdminSpec{
		Role: model.RoleReseller, Enabled: true, UsersLimit: 5,
		Permissions: map[string]bool{authz.InboundCreate: true, authz.SecClients: false},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	v, err := s.Get(created.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.UsersLimit != 5 || !v.Permissions[authz.InboundCreate] || v.Permissions[authz.SecClients] {
		t.Fatalf("view after update = %+v", v)
	}
}

// A reset must end the target's open sessions, or a dismissed reseller keeps
// working from the browser tab they already have open.
func TestResetPasswordBumpsLoginEpoch(t *testing.T) {
	s := newAdminService(t)
	created, err := s.Create(reseller("agent", "pw-agent-123"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before := reload(t, created.Id)
	if err := s.ResetPassword(created.Id, "fresh-pw-789"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	after := reload(t, created.Id)
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
	created, err := s.Create(reseller("agent", "pw-agent-123"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Update(created.Id, AdminSpec{Role: model.RoleReseller, Enabled: false}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if reload(t, created.Id).LoginEpoch == 0 {
		t.Fatal("disabling left the login epoch untouched")
	}
}

// Assignment is replace-all, deduplicated, and refuses inbounds that do not
// exist, so the admins page can send its whole picker state on every save.
func TestSetAssignmentsReplacesDedupesAndValidates(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	agent, err := s.Create(reseller("agent", "pw-agent-123"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a := seedInbound(t, me, "a")
	b := seedInbound(t, me, "b")

	if err := s.SetAssignments(agent.Id, []int{a, a, b}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	got, err := s.Assignments(agent.Id)
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("assignments = %v, want [%d %d]", got, a, b)
	}

	if err := s.SetAssignments(agent.Id, []int{b}); err != nil {
		t.Fatalf("second set: %v", err)
	}
	got, _ = s.Assignments(agent.Id)
	if len(got) != 1 || got[0] != b {
		t.Fatalf("after replace = %v, want [%d]", got, b)
	}

	if err := s.SetAssignments(agent.Id, []int{b, 9999}); err == nil {
		t.Fatal("assignment of a nonexistent inbound was accepted")
	}
	got, _ = s.Assignments(agent.Id)
	if len(got) != 1 || got[0] != b {
		t.Fatalf("a failed set must leave assignments untouched, got %v", got)
	}

	if err := s.SetAssignments(9999, []int{b}); err == nil {
		t.Fatal("assignment to a nonexistent admin was accepted")
	}
}
