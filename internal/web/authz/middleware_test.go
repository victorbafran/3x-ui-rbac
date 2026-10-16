package authz

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// newAuthzEnv builds the real middleware over a real DB, with a login route so
// the caller is established through a cookie exactly as the panel does it.
func newAuthzEnv(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	dbtest.InitDB(t, filepath.Join(dir, "x-ui.db"))

	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("authz-test"))))
	engine.GET("/login-as/:username", func(c *gin.Context) {
		var u model.User
		if err := database.GetDB().Where("username = ?", c.Param("username")).First(&u).Error; err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		if err := session.SetLoginUser(c, &u); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
	api := engine.Group("/panel/api")
	api.Use(Middleware(api.BasePath()))
	reached := func(c *gin.Context) { c.String(http.StatusOK, "reached") }
	api.POST("/inbounds/del/:id", reached)
	api.POST("/inbounds/update/:id", reached)
	api.GET("/inbounds/list", reached)
	api.GET("/server/status", reached)
	api.GET("/server/getDb", reached)

	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return srv, &http.Client{Jar: jar}
}

func seedUser(t *testing.T, username string, role model.Role) int {
	t.Helper()
	u := model.User{Username: username, Password: "x", Role: role, Enabled: true}
	if err := database.GetDB().Create(&u).Error; err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return u.Id
}

func seedInbound(t *testing.T, ownerId int, tag string) int {
	t.Helper()
	ib := model.Inbound{UserId: ownerId, Tag: tag, Protocol: model.VLESS, Remark: tag,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := database.GetDB().Create(&ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	return ib.Id
}

// flipSwitch stores one override for an admin, the way the admins page will.
func flipSwitch(t *testing.T, userId int, key string, on bool) {
	t.Helper()
	raw := EncodeOverrides(map[string]bool{key: on})
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", userId).Update("permissions", raw).Error; err != nil {
		t.Fatalf("flip %s: %v", key, err)
	}
}

func login(t *testing.T, c *http.Client, srv *httptest.Server, username string) {
	t.Helper()
	if code := do(t, c, http.MethodGet, srv.URL+"/login-as/"+username); code != http.StatusOK {
		t.Fatalf("login %s: got %d", username, code)
	}
}

func do(t *testing.T, c *http.Client, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// Three gates in one flow. Another admin's inbound is refused on ownership.
// The owner's own inbound is still refused because the reseller template has
// no delete switch. Flipping that one switch for that one admin opens it.
func TestResellerDeleteNeedsOwnershipAndTheSwitch(t *testing.T) {
	srv, client := newAuthzEnv(t)
	ownerId := seedUser(t, "owner", model.RoleReseller)
	seedUser(t, "intruder", model.RoleReseller)
	inbound := seedInbound(t, ownerId, "owned-by-owner")
	url := srv.URL + "/panel/api/inbounds/del/" + itoa(inbound)

	login(t, client, srv, "intruder")
	if code := do(t, client, http.MethodPost, url); code != http.StatusForbidden {
		t.Fatalf("intruder deleting inbound %d: got %d, want 403", inbound, code)
	}

	login(t, client, srv, "owner")
	if code := do(t, client, http.MethodPost, url); code != http.StatusForbidden {
		t.Fatalf("owner without inbounds.delete: got %d, want 403", code)
	}
	flipSwitch(t, ownerId, InboundDelete, true)
	if code := do(t, client, http.MethodPost, url); code != http.StatusOK {
		t.Fatalf("owner with inbounds.delete: got %d, want 200", code)
	}
}

// Assignment is how a super_admin hands an existing inbound to a reseller.
// Until the row exists the inbound is foreign; once it does, the reseller's
// own switches decide what they may do with it.
func TestAssignedInboundBecomesEditable(t *testing.T) {
	srv, client := newAuthzEnv(t)
	bossId := seedUser(t, "boss", model.RoleSuperAdmin)
	agentId := seedUser(t, "agent", model.RoleReseller)
	flipSwitch(t, agentId, InboundEdit, true)
	inbound := seedInbound(t, bossId, "built-by-boss")
	url := srv.URL + "/panel/api/inbounds/update/" + itoa(inbound)

	login(t, client, srv, "agent")
	if code := do(t, client, http.MethodPost, url); code != http.StatusForbidden {
		t.Fatalf("agent on unassigned inbound: got %d, want 403", code)
	}
	if err := database.GetDB().Create(&model.AdminInbound{AdminId: agentId, InboundId: inbound}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}
	if code := do(t, client, http.MethodPost, url); code != http.StatusOK {
		t.Fatalf("agent on assigned inbound: got %d, want 200", code)
	}
}

// No switch exists for super_admin-only routes, so an override cannot open
// them; reading host metrics is deliberately open to every role.
func TestResellerReadsMetricsButCannotReachServerActions(t *testing.T) {
	srv, client := newAuthzEnv(t)
	agentId := seedUser(t, "agent", model.RoleReseller)
	flipSwitch(t, agentId, PermSuperOnly, true)
	login(t, client, srv, "agent")
	if code := do(t, client, http.MethodGet, srv.URL+"/panel/api/server/status"); code != http.StatusOK {
		t.Fatalf("reseller reading /server/status: got %d, want 200", code)
	}
	if code := do(t, client, http.MethodGet, srv.URL+"/panel/api/server/getDb"); code != http.StatusForbidden {
		t.Fatalf("reseller on /server/getDb despite a forged override: got %d, want 403", code)
	}
}

// A disabled admin keeps a valid session cookie; the gate must still refuse it.
func TestDisabledAdminIsRefused(t *testing.T) {
	srv, client := newAuthzEnv(t)
	id := seedUser(t, "suspended", model.RoleSuperAdmin)
	login(t, client, srv, "suspended")
	if code := do(t, client, http.MethodGet, srv.URL+"/panel/api/inbounds/list"); code != http.StatusOK {
		t.Fatalf("enabled super_admin listing: got %d, want 200", code)
	}
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", id).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	if code := do(t, client, http.MethodGet, srv.URL+"/panel/api/inbounds/list"); code != http.StatusForbidden {
		t.Fatalf("disabled super_admin listing: got %d, want 403", code)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
