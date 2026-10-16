package controller

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A reseller with a client limit is stopped at creation, before anything is
// written, once the clients they own plus the ones being added would pass it.
func TestCreateRefusesBeyondUsersLimit(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, ownInbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", resellerId).Update("users_limit", 1).Error; err != nil {
		t.Fatalf("set limit: %v", err)
	}
	engine := newGuardedClientEngine(t, loadActor(t, resellerId))

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "second@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "users limit") {
		t.Fatalf("second client with a limit of one: %d %s", rec.Code, rec.Body.String())
	}
	var n int64
	database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "second@example.com").Count(&n)
	if n != 0 {
		t.Fatal("client was created past the limit")
	}

	// Raising the limit opens the door; a zero limit means no limit at all.
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", resellerId).Update("users_limit", 0).Error; err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	engine = newGuardedClientEngine(t, loadActor(t, resellerId))
	rec = postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "second@example.com", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{ownInbound},
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("no limit yet refused: %s", rec.Body.String())
	}
}
