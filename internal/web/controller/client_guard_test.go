package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
)

// The body-addressed client routes run after the authz gate, which can only
// check the URL. This harness plants the gate's actor directly and drives the
// real handlers, so the body checks are exercised end to end.
func newGuardedClientEngine(t *testing.T, actor *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authz.ActorKey, actor)
		c.Next()
	})
	NewClientController(engine.Group("/panel/api/clients"))
	return engine
}

func seedAdminWithInbound(t *testing.T, username, tag, email string) (adminId, inboundId int) {
	t.Helper()
	db := database.GetDB()
	u := model.User{Username: username, Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	ib := model.Inbound{UserId: u.Id, Tag: tag, Remark: tag, Protocol: model.VLESS, Settings: `{"clients":[]}`,
		StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatalf("inbound: %v", err)
	}
	cr := model.ClientRecord{Email: email, Enable: true, AdminId: u.Id}
	if err := db.Create(&cr).Error; err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: cr.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatalf("attach: %v", err)
	}
	return u.Id, ib.Id
}

func postJSON(t *testing.T, engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// A reseller posting bulkDel with another admin's email must get 403 and the
// client must still exist. Before the guard, the handler deleted it.
func TestBulkDeleteRefusesForeignClientAndLeavesItIntact(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, _ := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	seedAdminWithInbound(t, "other", "other-tag", "theirs@example.com")
	var actor model.User
	if err := database.GetDB().First(&actor, resellerId).Error; err != nil {
		t.Fatalf("actor: %v", err)
	}
	engine := newGuardedClientEngine(t, &actor)

	rec := postJSON(t, engine, "/panel/api/clients/bulkDel", map[string]any{"emails": []string{"theirs@example.com"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign bulkDel: status %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	var n int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "theirs@example.com").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("foreign client was deleted (count %d)", n)
	}

	// Own client passes the guard; whatever the service then says, it is not a 403.
	rec = postJSON(t, engine, "/panel/api/clients/bulkDel", map[string]any{"emails": []string{"mine@example.com"}})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("own client refused: %s", rec.Body.String())
	}
}

// Creating a client names target inbounds in the body: a reseller may not
// place a client on an inbound they neither created nor were assigned.
func TestCreateRefusesForeignInbound(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, _ := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	_, foreignInbound := seedAdminWithInbound(t, "other", "other-tag", "theirs@example.com")
	var actor model.User
	if err := database.GetDB().First(&actor, resellerId).Error; err != nil {
		t.Fatalf("actor: %v", err)
	}
	engine := newGuardedClientEngine(t, &actor)

	rec := postJSON(t, engine, "/panel/api/clients/add", map[string]any{
		"email": "new@example.com", "inboundIds": []int{foreignInbound},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create on foreign inbound: status %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	var n int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "new@example.com").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("client was created on a foreign inbound")
	}
}

// Collection reads are filtered, not refused: a reseller asking who is online
// must not learn other admins' client emails.
func TestLastOnlineHidesOtherAdminsClients(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, _ := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	seedAdminWithInbound(t, "other", "other-tag", "theirs@example.com")
	// last_online lives in client_traffics rows; seed one per client.
	for _, email := range []string{"mine@example.com", "theirs@example.com"} {
		if err := database.GetDB().Exec("INSERT INTO client_traffics (email, last_online, enable) VALUES (?, ?, ?)", email, 1700000000000, true).Error; err != nil {
			t.Fatalf("seed traffic %s: %v", email, err)
		}
	}
	var actor model.User
	if err := database.GetDB().First(&actor, resellerId).Error; err != nil {
		t.Fatalf("actor: %v", err)
	}
	engine := newGuardedClientEngine(t, &actor)

	rec := postJSON(t, engine, "/panel/api/clients/lastOnline", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("lastOnline: status %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Obj map[string]int64 `json:"obj"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, leaked := resp.Obj["theirs@example.com"]; leaked {
		t.Fatalf("another admin's client leaked through lastOnline: %v", resp.Obj)
	}
	if _, ok := resp.Obj["mine@example.com"]; !ok {
		t.Fatalf("own client missing from lastOnline: %v", resp.Obj)
	}
}
