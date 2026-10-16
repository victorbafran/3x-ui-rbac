package controller

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func clientRow(t *testing.T, email string) model.ClientRecord {
	t.Helper()
	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", email).First(&rec).Error; err != nil {
		t.Fatalf("client %s: %v", email, err)
	}
	return rec
}

func seedReseller(t *testing.T, username string) int {
	t.Helper()
	u := model.User{Username: username, Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := database.GetDB().Create(&u).Error; err != nil {
		t.Fatalf("reseller %s: %v", username, err)
	}
	return u.Id
}

func assign(t *testing.T, adminId, inboundId int) {
	t.Helper()
	if err := database.GetDB().Create(&model.AdminInbound{AdminId: adminId, InboundId: inboundId}).Error; err != nil {
		t.Fatalf("assign %d->%d: %v", adminId, inboundId, err)
	}
}

// Two resellers sharing one inbound must not share customers. Before clients
// had an owner, both saw the whole list and could edit every row in it.
func TestClientsAreOwnedByTheirCreatorNotTheInbound(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	_, shared := seedAdminWithInbound(t, "boss2", "shared-tag", "seed@example.com")
	oneId := seedReseller(t, "one")
	twoId := seedReseller(t, "two")
	assign(t, oneId, shared)
	assign(t, twoId, shared)

	// Each reseller creates a customer under the same display name.
	for _, id := range []int{oneId, twoId} {
		engine := newGuardedClientEngine(t, loadActor(t, id))
		rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
			"client": map[string]any{"email": "ali", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("reseller %d creating ali: %d %s", id, rec.Code, rec.Body.String())
		}
	}

	first := clientRow(t, "a"+strconv.Itoa(oneId)+":ali")
	second := clientRow(t, "a"+strconv.Itoa(twoId)+":ali")
	if first.AdminId != oneId {
		t.Fatalf("first ali owned by %d, want %d", first.AdminId, oneId)
	}
	if second.AdminId != twoId {
		t.Fatalf("second ali owned by %d, want %d", second.AdminId, twoId)
	}

	// Neither reseller may reach the other's, though they share the inbound.
	for _, pair := range []struct{ me, other int }{{oneId, twoId}, {twoId, oneId}} {
		engine := newGuardedClientEngine(t, loadActor(t, pair.me))
		rec := postJSON(t, engine, "/panel/api/clients/bulkDel", map[string]any{
			"emails": []string{"a" + strconv.Itoa(pair.other) + ":ali"},
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("reseller %d deleting %d's client: %d %s", pair.me, pair.other, rec.Code, rec.Body.String())
		}
	}
}

// A client created before this change, or by a super_admin, belongs to nobody:
// the super_admin sees it and no reseller does.
func TestUnownedClientsStayWithTheSuperAdmin(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	_, shared := seedAdminWithInbound(t, "boss2", "shared-tag", "seed@example.com")
	resellerId := seedReseller(t, "one")
	assign(t, resellerId, shared)
	// A row from before clients had an owner, sitting on the shared inbound.
	legacy := model.ClientRecord{Email: "legacy@example.com", Enable: true}
	if err := database.GetDB().Create(&legacy).Error; err != nil {
		t.Fatalf("legacy client: %v", err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: legacy.Id, InboundId: shared}).Error; err != nil {
		t.Fatalf("attach legacy: %v", err)
	}
	if clientRow(t, "legacy@example.com").AdminId != 0 {
		t.Fatal("the legacy client should be unowned")
	}

	engine := newGuardedClientEngine(t, loadActor(t, resellerId))
	rec := postJSON(t, engine, "/panel/api/clients/bulkDel", map[string]any{"emails": []string{"legacy@example.com"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reseller reaching an unowned client: %d %s", rec.Code, rec.Body.String())
	}
}

// Stamping must never take a client that already has an owner.
func TestStampNeverStealsAnOwnedClient(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	_, shared := seedAdminWithInbound(t, "boss2", "shared-tag", "seed@example.com")
	oneId := seedReseller(t, "one")
	twoId := seedReseller(t, "two")
	assign(t, oneId, shared)
	assign(t, twoId, shared)

	engine := newGuardedClientEngine(t, loadActor(t, oneId))
	if rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client": map[string]any{"email": "ali", "totalGB": 10, "expiryTime": 1_900_000_000_000},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	owned := "a" + strconv.Itoa(oneId) + ":ali"

	// The second reseller re-posts the first one's stored email verbatim.
	engine2 := newGuardedClientEngine(t, loadActor(t, twoId))
	postJSON(t, engine2, "/panel/api/clients/add", map[string]any{
		"client": map[string]any{"email": owned, "totalGB": 10, "expiryTime": 1_900_000_000_000},
	})
	if got := clientRow(t, owned).AdminId; got != oneId {
		t.Fatalf("client changed hands to %d; it belongs to %d", got, oneId)
	}
}

// A super_admin's own clients are not namespaced: they are the panel's.
func TestSuperAdminClientsAreNotPrefixed(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	_, inbound := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	var boss model.User
	if err := database.GetDB().Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	engine := newGuardedClientEngine(t, &boss)
	if rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"client":     map[string]any{"email": "plain", "totalGB": 10, "expiryTime": 1_900_000_000_000},
		"inboundIds": []int{inbound},
	}); rec.Code != http.StatusOK {
		t.Fatalf("super admin create: %d %s", rec.Code, rec.Body.String())
	}
	if got := clientRow(t, "plain").AdminId; got != 0 {
		t.Fatalf("super_admin client stamped with owner %d; it should stay unowned", got)
	}
}
