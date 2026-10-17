package controller

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
)

func grantSwitch(t *testing.T, userId int, key string) {
	t.Helper()
	raw := authz.EncodeOverrides(map[string]bool{key: true})
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", userId).Update("permissions", raw).Error; err != nil {
		t.Fatalf("grant %s: %v", key, err)
	}
}

func loadActor(t *testing.T, id int) *model.User {
	t.Helper()
	var u model.User
	if err := database.GetDB().First(&u, id).Error; err != nil {
		t.Fatalf("actor: %v", err)
	}
	return &u
}

// The reseller template may create clients but may not hand out unlimited
// data or a never-expiring plan. Both switches are advertised in the admins
// page; this is what makes them true.
func TestCreateEnforcesUnlimitedSwitches(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, ownInbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	cases := []struct {
		name    string
		totalGB int64
		expiry  int64
		wantMsg string
	}{
		{"no data limit", 0, 1_900_000_000_000, authz.ClientUnlimitedData},
		{"never expires", 10, 0, authz.ClientUnlimitedExpiry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
				"client":     map[string]any{"email": "new@example.com", "totalGB": tc.totalGB, "expiryTime": tc.expiry},
				"inboundIds": []int{ownInbound},
			})
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("status %d body %s; want 403 naming %s", rec.Code, rec.Body.String(), tc.wantMsg)
			}
		})
	}

	// A delayed start (negative expiry) is a real limit, not "never".
	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "new@example.com", "totalGB": 10, "expiryTime": -86400000},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("delayed-start client refused: %s", rec.Body.String())
	}
}

// Flipping the switch for this one admin opens exactly that.
func TestUnlimitedSwitchOpensWhenGranted(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, ownInbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	grantSwitch(t, resellerId, authz.ClientUnlimitedData)
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "new@example.com", "totalGB": 0, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("unlimited data refused despite the switch: %s", rec.Body.String())
	}
	// The other switch is still off.
	rec = postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "new2@example.com", "totalGB": 0, "expiryTime": 0},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), authz.ClientUnlimitedExpiry) {
		t.Fatalf("never-expiring client accepted without its switch: %d %s", rec.Code, rec.Body.String())
	}
}

// Editing is the other way to remove a limit, so it is gated the same.
func TestUpdateEnforcesUnlimitedSwitches(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, _ := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	rec := postJSON(t, engine, "/panel/api/clients/update/mine@example.com", map[string]any{
		"email": "mine@example.com", "totalGB": 0, "expiryTime": 1_900_000_000_000,
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), authz.ClientUnlimitedData) {
		t.Fatalf("edit to unlimited data accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// A super_admin is never scoped by these switches.
func TestSuperAdminIgnoresUnlimitedSwitches(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	_, inbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	var boss model.User
	if err := database.GetDB().Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	engine := newGuardedClientEngine(t, &boss)

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "new@example.com", "totalGB": 0, "expiryTime": 0},
		"inboundIds": []int{inbound},
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("super_admin refused: %s", rec.Body.String())
	}
}
