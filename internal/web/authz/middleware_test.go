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
	api.GET("/inbounds/list", reached)
	api.GET("/server/status", reached)

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
	ib := model.Inbound{UserId: ownerId, Tag: tag, Protocol: model.VLESS, Port: 0, Remark: tag,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := database.GetDB().Create(&ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	return ib.Id
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

// A reseller must not delete an inbound belonging to another admin. Without the
// ownership check the handler is reached and the inbound is gone.
func TestResellerCannotTouchForeignInbound(t *testing.T) {
	srv, client := newAuthzEnv(t)
	ownerId := seedUser(t, "owner", model.RoleReseller)
	seedUser(t, "intruder", model.RoleReseller)
	foreign := seedInbound(t, ownerId, "owned-by-owner")

	if code := do(t, client, http.MethodGet, srv.URL+"/login-as/intruder"); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
	url := srv.URL + "/panel/api/inbounds/del/" + itoa(foreign)
	if code := do(t, client, http.MethodPost, url); code != http.StatusForbidden {
		t.Fatalf("intruder deleting inbound %d: got %d, want 403", foreign, code)
	}

	if code := do(t, client, http.MethodGet, srv.URL+"/login-as/owner"); code != http.StatusOK {
		t.Fatalf("login owner: got %d", code)
	}
	if code := do(t, client, http.MethodPost, url); code != http.StatusOK {
		t.Fatalf("owner deleting own inbound %d: got %d, want 200", foreign, code)
	}
}

// The role gate is separate from ownership: server endpoints belong to nobody,
// so only super_admin may reach them however many inbounds a reseller owns.
func TestResellerCannotReachServerEndpoints(t *testing.T) {
	srv, client := newAuthzEnv(t)
	seedUser(t, "agent", model.RoleReseller)
	if code := do(t, client, http.MethodGet, srv.URL+"/login-as/agent"); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
	if code := do(t, client, http.MethodGet, srv.URL+"/panel/api/server/status"); code != http.StatusForbidden {
		t.Fatalf("reseller on /server/status: got %d, want 403", code)
	}
}

// A disabled admin keeps a valid session cookie; the gate must still refuse it.
func TestDisabledAdminIsRefused(t *testing.T) {
	srv, client := newAuthzEnv(t)
	id := seedUser(t, "suspended", model.RoleSuperAdmin)
	if code := do(t, client, http.MethodGet, srv.URL+"/login-as/suspended"); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
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
