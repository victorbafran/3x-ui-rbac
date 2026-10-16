package controller

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func attachedInboundIds(t *testing.T, email string) []int {
	t.Helper()
	var ids []int
	err := database.GetDB().Model(&model.ClientInbound{}).
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", email).
		Order("inbound_id ASC").
		Pluck("inbound_id", &ids).Error
	if err != nil {
		t.Fatalf("attached inbounds for %s: %v", email, err)
	}
	return ids
}

// A super_admin can take the inbounds page away from a reseller, and then the
// reseller cannot pick an inbound -- but creating a customer still has to
// work. The inbounds they were assigned stand in for the choice.
func TestCreateWithoutInboundIdsUsesTheAssignedOnes(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, ownInbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	bossId, bossInbound := seedAdminWithInbound(t, "boss", "boss-tag", "theirs@example.com")
	_ = bossId
	// One inbound the super_admin handed over, on top of the one they created.
	if err := database.GetDB().Create(&model.AdminInbound{AdminId: resellerId, InboundId: bossInbound}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client": map[string]any{"email": "new@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create with no inboundIds: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success {
		t.Fatalf("create reported failure: %s", rec.Body.String())
	}
	got := attachedInboundIds(t, prefixedEmail(resellerId, "new@example.com"))
	if len(got) != 2 || got[0] != ownInbound || got[1] != bossInbound {
		t.Fatalf("client landed on %v, want both owned inbounds [%d %d]", got, ownInbound, bossInbound)
	}
}

// An explicit choice is still honoured, and still checked for ownership.
func TestExplicitInboundIdsAreNotOverridden(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, ownInbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	_, foreignInbound := seedAdminWithInbound(t, "boss", "boss-tag", "theirs@example.com")
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "new@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit own inbound: %d %s", rec.Code, rec.Body.String())
	}
	if got := attachedInboundIds(t, prefixedEmail(resellerId, "new@example.com")); len(got) != 1 || got[0] != ownInbound {
		t.Fatalf("client landed on %v, want only [%d]", got, ownInbound)
	}

	// The fallback must not become a way around the ownership check.
	rec = postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "other@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{foreignInbound},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("explicit foreign inbound: %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// A super_admin naming no inbound is a real mistake, not an implicit choice:
// they own every inbound, so filling them all in would be a surprise.
func TestSuperAdminStillMustNameAnInbound(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	var boss model.User
	if err := database.GetDB().Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	engine := newGuardedClientEngine(t, &boss)

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client": map[string]any{"email": "new@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
	})
	if rec.Code == http.StatusOK && len(attachedInboundIds(t, "new@example.com")) > 0 {
		t.Fatal("super_admin's empty inboundIds were filled in silently")
	}
}
