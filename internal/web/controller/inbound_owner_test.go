package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
)

const (
	bossSecret    = "boss-secret-uuid"
	bossCustomer  = "boss-customer@example.com"
	ownedCustomer = "a9:mine@example.com"
	sharedTgId    = 7070
)

type sharedFixture struct {
	boss, reseller *model.User
	inbound        int
}

// seedSharedInbound is a super_admin's disabled inbound shared with a reseller,
// one client each on it; both clients carry the same Telegram id.
func seedSharedInbound(t *testing.T) sharedFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	dbtest.InitDB(t, filepath.Join(dir, "x-ui.db"))
	prev := runtime.GetManager()
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}}))
	t.Cleanup(func() { runtime.SetManager(prev) })

	db := database.GetDB()
	var boss model.User
	if err := db.Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	reseller := loadActor(t, seedReseller(t, "res-shared"))
	settings := `{"clients":[{"id":"` + bossSecret + `","email":"` + bossCustomer + `","enable":true},` +
		`{"id":"mine-uuid","email":"` + ownedCustomer + `","enable":true}],"decryption":"none"}`
	ib := model.Inbound{
		UserId: boss.Id, Tag: "shared-owner", Remark: "shared-owner", Protocol: model.VLESS, Port: 40201,
		Settings: settings, StreamSettings: `{"network":"tcp","security":"none"}`, Sniffing: "{}",
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatalf("inbound: %v", err)
	}
	assign(t, reseller.Id, ib.Id)
	for _, c := range []struct {
		admin       int
		email, uuid string
	}{{0, bossCustomer, bossSecret}, {reseller.Id, ownedCustomer, "mine-uuid"}} {
		rec := model.ClientRecord{Email: c.email, UUID: c.uuid, Enable: true, AdminId: c.admin, TgID: sharedTgId}
		if err := db.Create(&rec).Error; err != nil {
			t.Fatalf("client %s: %v", c.email, err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
			t.Fatalf("attach %s: %v", c.email, err)
		}
	}
	return sharedFixture{boss: &boss, reseller: reseller, inbound: ib.Id}
}

func newOwnerEngine(actor *model.User) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authz.ActorKey, actor)
		session.SetAPIAuthUser(c, actor)
		c.Next()
	})
	NewInboundController(engine.Group("/panel/api/inbounds"))
	clients := engine.Group("/panel/api/clients")
	NewClientController(clients)
	NewGroupController(clients)
	return engine
}

func serve(engine *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func storedSettings(t *testing.T, id int) string {
	t.Helper()
	var ib model.Inbound
	if err := database.GetDB().First(&ib, id).Error; err != nil {
		t.Fatalf("inbound %d: %v", id, err)
	}
	return ib.Settings
}

// The handlers must ask for the caller's view, not the whole inbound: the
// export window builds its JSON from /get/:id.
func TestInboundReadRoutesHandAResellerOnlyTheirClients(t *testing.T) {
	f := seedSharedInbound(t)
	engine := newOwnerEngine(f.reseller)
	for _, path := range []string{"/list", "/list/slim", "/get/" + strconv.Itoa(f.inbound)} {
		rec := serve(engine, httptest.NewRequest(http.MethodGet, "/panel/api/inbounds"+path, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, ownedCustomer) {
			t.Fatalf("%s: %d %s; want the reseller's own client", path, rec.Code, body)
		}
		if strings.Contains(body, bossSecret) || strings.Contains(body, bossCustomer) {
			t.Fatalf("%s handed the reseller the other admin's client: %s", path, body)
		}
	}
}

type hubOnlyServer struct{ hub *websocket.Hub }

func (s hubOnlyServer) GetCron() *cron.Cron     { return nil }
func (s hubOnlyServer) GetCtx() context.Context { return context.Background() }
func (s hubOnlyServer) GetWSHub() any           { return s.hub }

// The hub sends one message to every open panel, whoever is signed in there. An
// inbound list pushed after a super_admin's edit reached each reseller's browser.
func TestInboundChangesPushNoInboundListOverTheWebSocket(t *testing.T) {
	f := seedSharedInbound(t)
	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	prev := global.GetWebServer()
	global.SetWebServer(hubOnlyServer{hub: hub})
	t.Cleanup(func() { global.SetWebServer(prev) })
	resellerTab := websocket.NewClient("reseller-tab")
	hub.Register(resellerTab)
	for deadline := time.Now().Add(2 * time.Second); hub.GetClientCount() != 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("websocket client never registered")
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/bulkDel", strings.NewReader(`{"ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := serve(newOwnerEngine(f.boss), req); rec.Code != http.StatusOK {
		t.Fatalf("bulkDel: %d %s", rec.Code, rec.Body.String())
	}
	sawInboundsInvalidate := false
	for range 2 {
		select {
		case raw := <-resellerTab.Send:
			var msg struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatalf("message %s: %v", raw, err)
			}
			if msg.Type == string(websocket.MessageTypeInbounds) || strings.Contains(string(raw), bossSecret) {
				t.Fatalf("an inbound list went to every open panel: %s", raw)
			}
			sawInboundsInvalidate = sawInboundsInvalidate || string(msg.Payload) == `{"type":"inbounds"}`
		case <-time.After(2 * time.Second):
			t.Fatal("no websocket message after an inbound change")
		}
	}
	if !sawInboundsInvalidate {
		t.Fatal("open panels were not told to re-fetch their inbounds")
	}
}

// sharedUpdateRequest is the edit form a reseller's panel posts for the shared
// inbound: only the reseller's own client is in it.
func sharedUpdateRequest(id int) *http.Request {
	form := url.Values{
		"protocol":       {"vless"},
		"port":           {"40201"},
		"tag":            {"shared-owner"},
		"remark":         {"renamed"},
		"enable":         {"false"},
		"settings":       {`{"clients":[{"id":"mine-uuid","email":"` + ownedCustomer + `","enable":true}],"decryption":"none"}`},
		"streamSettings": {`{"network":"tcp","security":"none"}`},
		"sniffing":       {"{}"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/update/"+strconv.Itoa(id), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// Marked as a master's push, an edit skips keepStoredClients and saves the clients
// it posts. A reseller's form holds only their own, so the mark dropped the rest.
func TestResellerMasterPushMarkCannotReplaceTheClients(t *testing.T) {
	f := seedSharedInbound(t)
	req := sharedUpdateRequest(f.inbound)
	req.Header.Set(wirecodec.MasterPushHeader, "1")
	rec := serve(newOwnerEngine(f.reseller), req)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if got := storedSettings(t, f.inbound); !strings.Contains(got, bossCustomer) || !strings.Contains(got, ownedCustomer) {
		t.Fatalf("a reseller's save replaced the inbound's clients: %s", got)
	}
}

// The save puts every admin's clients back into the row it returns, so pressing
// Save handed a reseller the other admins' credentials in the reply.
func TestInboundUpdateReplyCarriesOnlyTheCallersClients(t *testing.T) {
	f := seedSharedInbound(t)
	rec := serve(newOwnerEngine(f.reseller), sharedUpdateRequest(f.inbound))
	body := rec.Body.String()
	if !strings.Contains(body, `"success":true`) || !strings.Contains(body, ownedCustomer) {
		t.Fatalf("reseller update: %d %s; want success with their own client", rec.Code, body)
	}
	if strings.Contains(body, bossSecret) || strings.Contains(body, bossCustomer) {
		t.Fatalf("the update reply handed the reseller the other admin's client: %s", body)
	}

	rec = serve(newOwnerEngine(f.boss), sharedUpdateRequest(f.inbound))
	var reply struct {
		Success bool `json:"success"`
		Obj     struct {
			Settings json.RawMessage `json:"settings"`
		} `json:"obj"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || !reply.Success {
		t.Fatalf("super_admin update: %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	var stored bytes.Buffer
	if err := json.Compact(&stored, []byte(storedSettings(t, f.inbound))); err != nil {
		t.Fatalf("stored settings: %v", err)
	}
	if string(reply.Obj.Settings) != stored.String() {
		t.Fatalf("super_admin reply settings changed:\n got %s\nwant %s", reply.Obj.Settings, stored.String())
	}
}

// inboundFields is a disabled VLESS inbound carrying one client, as the add form posts it.
func inboundFields(port int, email string) url.Values {
	return url.Values{
		"protocol":       {"vless"},
		"port":           {strconv.Itoa(port)},
		"tag":            {"own-" + strconv.Itoa(port)},
		"remark":         {"own"},
		"enable":         {"false"},
		"settings":       {`{"clients":[{"id":"` + email + `-uuid","email":"` + email + `","enable":true}],"decryption":"none"}`},
		"streamSettings": {`{"network":"tcp","security":"none"}`},
		"sniffing":       {"{}"},
	}
}

// Clients embedded in an added or imported inbound were stored without an owner,
// so the reseller who brought them in could no longer see or manage them.
func TestResellerAddedAndImportedInboundsKeepTheirClients(t *testing.T) {
	f := seedSharedInbound(t)
	for _, tc := range []struct {
		name, email string
		form        func() url.Values
		path        string
	}{
		{"add", "added@example.com", func() url.Values { return inboundFields(40211, "added@example.com") }, "/add"},
		{"import", "imported@example.com", func() url.Values {
			fields := inboundFields(40212, "imported@example.com")
			data, _ := json.Marshal(map[string]any{
				"protocol": "vless", "port": 40212, "tag": fields.Get("tag"), "remark": "own", "enable": false,
				"settings": fields.Get("settings"), "streamSettings": fields.Get("streamSettings"), "sniffing": "{}",
			})
			return url.Values{"data": {string(data)}}
		}, "/import"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds"+tc.path, strings.NewReader(tc.form().Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			engine := newOwnerEngine(f.reseller)
			if rec := serve(engine, req); !strings.Contains(rec.Body.String(), `"success":true`) {
				t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
			}
			var owner int
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", tc.email).Pluck("admin_id", &owner).Error; err != nil {
				t.Fatalf("owner of %s: %v", tc.email, err)
			}
			if owner != f.reseller.Id {
				t.Fatalf("%s stored with admin_id %d; want the reseller %d", tc.email, owner, f.reseller.Id)
			}
			if rec := serve(engine, httptest.NewRequest(http.MethodGet, "/panel/api/inbounds/list", nil)); !strings.Contains(rec.Body.String(), tc.email) {
				t.Fatalf("the reseller's inbound list lost %s: %s", tc.email, rec.Body.String())
			}
		})
	}
}

// A new inbound naming an existing client's email and subId attaches that client and
// merges the posted fields into it, so a reseller could rewrite another admin's client.
func TestResellerNewInboundCannotTakeAnotherAdminsClient(t *testing.T) {
	f := seedSharedInbound(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", bossCustomer).Update("sub_id", "boss-sub").Error; err != nil {
		t.Fatalf("sub id: %v", err)
	}
	fields := inboundFields(40213, "fresh@example.com")
	fields.Set("settings", `{"clients":[{"id":"hijack-uuid","email":"`+bossCustomer+`","subId":"boss-sub","enable":true},`+
		`{"id":"fresh-uuid","email":"fresh@example.com","enable":true}],"decryption":"none"}`)
	data, _ := json.Marshal(map[string]any{
		"protocol": "vless", "port": 40213, "tag": fields.Get("tag"), "remark": "own", "enable": false,
		"settings": fields.Get("settings"), "streamSettings": fields.Get("streamSettings"), "sniffing": "{}",
	})
	for _, tc := range []struct {
		path string
		form url.Values
	}{{"add", fields}, {"import", url.Values{"data": {string(data)}}}} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/"+tc.path, strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if rec := serve(newOwnerEngine(f.reseller), req); rec.Code != http.StatusForbidden {
				t.Fatalf("%s naming the other admin's client: %d %s; want 403", tc.path, rec.Code, rec.Body.String())
			}
			var boss model.ClientRecord
			if err := db.Where("email = ?", bossCustomer).First(&boss).Error; err != nil {
				t.Fatalf("boss client: %v", err)
			}
			if boss.UUID != bossSecret || boss.AdminId != 0 {
				t.Fatalf("the other admin's client changed: uuid %q admin_id %d", boss.UUID, boss.AdminId)
			}
		})
	}
}

// Group names are panel-wide, but the clients in a group are not: the emails
// route listed the other admins' customers and bulk edits moved them.
func TestGroupRoutesKeepToTheCallersClients(t *testing.T) {
	f := seedSharedInbound(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("email IN ?", []string{bossCustomer, ownedCustomer}).Update("group_name", "vip").Error; err != nil {
		t.Fatalf("group: %v", err)
	}
	engine := newOwnerEngine(f.reseller)
	emailsReq := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/panel/api/clients/groups/vip/emails", nil)
	}
	if rec := serve(engine, emailsReq()); !strings.Contains(rec.Body.String(), ownedCustomer) || strings.Contains(rec.Body.String(), bossCustomer) {
		t.Fatalf("group emails for the reseller: %d %s; want only their own client", rec.Code, rec.Body.String())
	}
	if rec := serve(newOwnerEngine(f.boss), emailsReq()); !strings.Contains(rec.Body.String(), bossCustomer) {
		t.Fatalf("super_admin group emails lost a client: %s", rec.Body.String())
	}
	for _, path := range []string{"groups/bulkAdd", "groups/bulkRemove"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/panel/api/clients/"+path, strings.NewReader(`{"emails":["`+bossCustomer+`"],"group":"other"}`))
			req.Header.Set("Content-Type", "application/json")
			if rec := serve(engine, req); rec.Code != http.StatusForbidden {
				t.Fatalf("%s on another admin's client: %d %s; want 403", path, rec.Code, rec.Body.String())
			}
			var group string
			if err := db.Model(&model.ClientRecord{}).Where("email = ?", bossCustomer).Pluck("group_name", &group).Error; err != nil || group != "vip" {
				t.Fatalf("the other admin's client moved to group %q (%v)", group, err)
			}
		})
	}
}

// "Delete all clients" on a shared inbound is the reseller's own clients only.
func TestResellerDelAllClientsKeepsTheOtherAdminsClients(t *testing.T) {
	f := seedSharedInbound(t)
	path := "/panel/api/inbounds/" + strconv.Itoa(f.inbound) + "/delAllClients"
	rec := serve(newOwnerEngine(f.reseller), httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delAllClients: %d %s", rec.Code, rec.Body.String())
	}
	count := func(email string) int64 {
		var n int64
		if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", email).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", email, err)
		}
		return n
	}
	if count(bossCustomer) != 1 {
		t.Fatalf("the reseller deleted the other admin's client; stored settings %s", storedSettings(t, f.inbound))
	}
	if count(ownedCustomer) != 0 {
		t.Fatalf("the reseller's own client survived delAllClients: %s", rec.Body.String())
	}
}

// authz admits a client on an inbound the caller shares, so the by-email routes
// handed a reseller the other admin's client, credentials included.
func TestByEmailRoutesRefuseAnotherAdminsClient(t *testing.T) {
	f := seedSharedInbound(t)
	manager := model.User{Username: "mgr-shared", Password: "x", Role: model.RoleManager, Enabled: true}
	if err := database.GetDB().Create(&manager).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	for _, tc := range []struct {
		name   string
		actor  *model.User
		email  string
		forbid bool
	}{
		{"reseller on the other admin's client", f.reseller, bossCustomer, true},
		{"reseller on their own client", f.reseller, ownedCustomer, false},
		{"manager, who is unscoped", &manager, bossCustomer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(newOwnerEngine(tc.actor), httptest.NewRequest(http.MethodGet, "/panel/api/clients/get/"+url.PathEscape(tc.email), nil))
			if tc.forbid {
				if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), bossSecret) {
					t.Fatalf("got %d %s; want 403 and no credentials", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.email) {
				t.Fatalf("got %d %s; want the client", rec.Code, rec.Body.String())
			}
		})
	}
}

// A Telegram id is not a secret, yet the lookup returned every client under it.
func TestClientsByTgIdCarryOnlyTheCallersOwn(t *testing.T) {
	f := seedSharedInbound(t)
	path := "/panel/api/clients/get/tgId/" + strconv.Itoa(sharedTgId)
	rec := serve(newOwnerEngine(f.reseller), httptest.NewRequest(http.MethodGet, path, nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, ownedCustomer) {
		t.Fatalf("tgId lookup: %d %s; want the reseller's client", rec.Code, body)
	}
	if strings.Contains(body, bossCustomer) || strings.Contains(body, bossSecret) {
		t.Fatalf("tgId lookup handed the reseller another admin's client: %s", body)
	}
	if rec := serve(newOwnerEngine(f.boss), httptest.NewRequest(http.MethodGet, path, nil)); !strings.Contains(rec.Body.String(), bossCustomer) {
		t.Fatalf("super_admin lookup lost a client: %s", rec.Body.String())
	}
}
