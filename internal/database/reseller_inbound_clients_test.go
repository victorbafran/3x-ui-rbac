package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// resellerInboundFixture is a panel from before rbac43: a reseller's clients added
// inside an inbound were saved with admin_id 0, the same as the super_admin's own.
type resellerInboundFixture struct {
	superAdmin, manager, reseller1, reseller2 int
	reseller1Inbound                          int
}

func seedRoleAdmin(t *testing.T, username string, role model.Role) int {
	t.Helper()
	u := model.User{Username: username, Password: "x", Role: role, Enabled: true}
	if err := GetDB().Create(&u).Error; err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return u.Id
}

func assignInbound(t *testing.T, adminId, inboundId int) {
	t.Helper()
	if err := GetDB().Create(&model.AdminInbound{AdminId: adminId, InboundId: inboundId}).Error; err != nil {
		t.Fatalf("assign inbound %d to %d: %v", inboundId, adminId, err)
	}
}

// Client timestamps: the fixture's unowned clients predate rbac43, and r2-held is
// the first client rbac43 stamped as a reseller's.
const (
	beforeRbac43    int64 = 1_000
	firstStamped    int64 = 2_000
	afterFirstStamp int64 = 3_000
)

func seedClientIn(t *testing.T, email string, adminId int, createdAt int64, inboundIds ...int) {
	t.Helper()
	c := model.ClientRecord{Email: email, AdminId: adminId, Enable: true, CreatedAt: createdAt}
	if err := GetDB().Create(&c).Error; err != nil {
		t.Fatalf("create client %s: %v", email, err)
	}
	for _, id := range inboundIds {
		if err := GetDB().Create(&model.ClientInbound{ClientId: c.Id, InboundId: id}).Error; err != nil {
			t.Fatalf("attach %s to inbound %d: %v", email, id, err)
		}
	}
}

func seedResellerInboundFixture(t *testing.T) resellerInboundFixture {
	t.Helper()
	f := resellerInboundFixture{
		superAdmin: seedRoleAdmin(t, "sa", model.RoleSuperAdmin),
		manager:    seedRoleAdmin(t, "mgr", model.RoleManager),
		reseller1:  seedRoleAdmin(t, "r1", model.RoleReseller),
		reseller2:  seedRoleAdmin(t, "r2", model.RoleReseller),
	}
	const deletedReseller = 9999
	saIn := seedInboundFor(t, f.superAdmin, "sa-in")
	mgrIn := seedInboundFor(t, f.manager, "mgr-in")
	f.reseller1Inbound = seedInboundFor(t, f.reseller1, "r1-in")
	r1Second := seedInboundFor(t, f.reseller1, "r1-second")
	assignInbound(t, f.reseller1, r1Second)
	r2In := seedInboundFor(t, f.reseller2, "r2-in")
	r1SharedWithR2 := seedInboundFor(t, f.reseller1, "r1-shared")
	assignInbound(t, f.reseller2, r1SharedWithR2)
	saAssignedR1 := seedInboundFor(t, f.superAdmin, "sa-assigned-r1")
	assignInbound(t, f.reseller1, saAssignedR1)
	goneIn := seedInboundFor(t, deletedReseller, "gone-in")

	// r2-one and r1-two interleave, so a cutoff re-read after one reseller's stamp
	// would cut the other reseller's.
	seedClientIn(t, "r1-one", 0, beforeRbac43, f.reseller1Inbound)
	seedClientIn(t, "r1-two", 0, beforeRbac43+600, f.reseller1Inbound, r1Second)
	seedClientIn(t, "r2-one", 0, beforeRbac43+500, r2In)
	seedClientIn(t, "r1-and-sa", 0, beforeRbac43, f.reseller1Inbound, saIn)
	seedClientIn(t, "r1-and-r2", 0, beforeRbac43, f.reseller1Inbound, r2In)
	seedClientIn(t, "sa-own", 0, beforeRbac43, saIn)
	seedClientIn(t, "mgr-own", 0, beforeRbac43, mgrIn)
	seedClientIn(t, "no-inbound", 0, beforeRbac43)
	seedClientIn(t, "shared", 0, beforeRbac43, r1SharedWithR2)
	seedClientIn(t, "assigned", 0, beforeRbac43, saAssignedR1)
	seedClientIn(t, "gone", 0, beforeRbac43, goneIn)
	seedClientIn(t, "r2-held", f.reseller2, firstStamped, f.reseller1Inbound)
	return f
}

func clientOwners(t *testing.T) map[string]int {
	t.Helper()
	var rows []model.ClientRecord
	if err := GetDB().Select("email", "admin_id").Find(&rows).Error; err != nil {
		t.Fatalf("read client owners: %v", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Email] = r.AdminId
	}
	return out
}

func reopenDB(t *testing.T, dbPath string) {
	t.Helper()
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("reopen %s: %v", dbPath, err)
	}
}

// openUpgradedPanel is a database last opened by a build before this step: the
// fixture is in place and the step's history row is not.
func openUpgradedPanel(t *testing.T) (string, resellerInboundFixture) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	f := seedResellerInboundFixture(t)
	clearSeederHistory(t, resellerInboundClientsSeeder)
	return dbPath, f
}

// An unowned client goes to a reseller only when every inbound it sits in is that
// reseller's alone; any doubt leaves it with the super_admin, who sees everything.
func TestResellerInboundClientsGoBackToTheirReseller(t *testing.T) {
	dbPath, f := openUpgradedPanel(t)
	reopenDB(t, dbPath)
	got := clientOwners(t)

	for _, tc := range []struct {
		name, email string
		want        int
	}{
		{"one inbound the reseller created", "r1-one", f.reseller1},
		{"two inbounds of the same reseller, one also self-assigned", "r1-two", f.reseller1},
		{"a second reseller gets their own, not the first's id", "r2-one", f.reseller2},
		{"also in a super_admin inbound", "r1-and-sa", 0},
		{"in inbounds of two different resellers", "r1-and-r2", 0},
		{"the super_admin's own client", "sa-own", 0},
		{"a manager's inbound", "mgr-own", 0},
		{"attached to no inbound", "no-inbound", 0},
		{"the reseller's inbound is also assigned to another reseller", "shared", 0},
		{"a super_admin inbound assigned to the reseller", "assigned", 0},
		{"the inbound's reseller was deleted", "gone", 0},
		{"already owned by another reseller", "r2-held", f.reseller2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got[tc.email] != tc.want {
				t.Fatalf("%s admin_id = %d, want %d", tc.email, got[tc.email], tc.want)
			}
		})
	}
}

// After the step an unowned client in a reseller's inbound is one the super_admin
// put there, so no later boot may hand it to the reseller.
func TestResellerInboundClientsStampRunsOnce(t *testing.T) {
	t.Run("a fresh install records the step at first boot", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "x-ui.db")
		if err := InitDB(dbPath); err != nil {
			t.Fatalf("InitDB: %v", err)
		}
		t.Cleanup(func() { _ = CloseDB() })
		seedResellerInboundFixture(t)

		reopenDB(t, dbPath)
		if got := clientOwners(t)["r1-one"]; got != 0 {
			t.Fatalf("r1-one admin_id = %d after a reboot of a fresh install, want 0", got)
		}
	})

	t.Run("a second boot changes nothing", func(t *testing.T) {
		dbPath, f := openUpgradedPanel(t)
		reopenDB(t, dbPath)
		first := clientOwners(t)
		if first["r1-one"] != f.reseller1 {
			t.Fatalf("first boot: r1-one admin_id = %d, want %d", first["r1-one"], f.reseller1)
		}
		seedClientIn(t, "added-by-super-admin", 0, beforeRbac43, f.reseller1Inbound)

		reopenDB(t, dbPath)
		second := clientOwners(t)
		for email, want := range first {
			if second[email] != want {
				t.Errorf("%s admin_id = %d after the second boot, want %d", email, second[email], want)
			}
		}
		if second["added-by-super-admin"] != 0 {
			t.Fatalf("added-by-super-admin admin_id = %d after the second boot, want 0", second["added-by-super-admin"])
		}
	})
}

// rbac43 stamps every client a reseller adds, so an unowned client newer than the
// first stamped one was an unscoped admin's, and stamping it gave it away.
func TestResellerInboundClientsStampStopsAtTheFirstOwnedClient(t *testing.T) {
	t.Run("a panel that ran rbac43", func(t *testing.T) {
		dbPath, f := openUpgradedPanel(t)
		seedClientIn(t, "sa-added-later", 0, afterFirstStamp, f.reseller1Inbound)
		seedClientIn(t, "sa-same-ms", 0, firstStamped, f.reseller1Inbound)
		seedClientIn(t, "r1-stamped-later", f.reseller1, afterFirstStamp+1_000, f.reseller1Inbound)
		reopenDB(t, dbPath)
		got := clientOwners(t)
		for email, want := range map[string]int{"r1-one": f.reseller1, "sa-added-later": 0, "sa-same-ms": 0} {
			if got[email] != want {
				t.Errorf("%s admin_id = %d, want %d", email, got[email], want)
			}
		}
	})

	t.Run("a panel from before rbac43 has no stamped client to stop at", func(t *testing.T) {
		dbPath, f := openUpgradedPanel(t)
		if err := GetDB().Where("email = ?", "r2-held").Delete(&model.ClientRecord{}).Error; err != nil {
			t.Fatalf("delete r2-held: %v", err)
		}
		seedClientIn(t, "r1-late", 0, afterFirstStamp, f.reseller1Inbound)
		reopenDB(t, dbPath)
		if got := clientOwners(t)["r1-late"]; got != f.reseller1 {
			t.Fatalf("r1-late admin_id = %d, want %d", got, f.reseller1)
		}
	})
}
