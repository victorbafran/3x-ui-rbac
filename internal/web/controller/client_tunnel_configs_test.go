package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// tunnelConfigRecorder notes whose tunnel configs, and which inbounds' links, it renders.
type tunnelConfigRecorder struct {
	emails   []string
	inbounds []int
}

func (p *tunnelConfigRecorder) SubLinksForSubId(string, string) ([]string, error) { return nil, nil }

func (p *tunnelConfigRecorder) LinksForClient(string, *model.Inbound, string) []string { return nil }

func (p *tunnelConfigRecorder) LinksForInbounds(string, []*model.Inbound) []string { return nil }

func (p *tunnelConfigRecorder) InboundClientLinks(_ string, inbound *model.Inbound, _ []string) []service.InboundClientLinks {
	p.inbounds = append(p.inbounds, inbound.Id)
	return nil
}

func (p *tunnelConfigRecorder) TunnelConfigsForClient(_ string, _ []*model.Inbound, email string) []service.TunnelConfig {
	p.emails = append(p.emails, email)
	return nil
}

// A tunnel config carries the client's private key. A reseller naming another
// admin's client by its row id is refused before anything renders it.
func TestTunnelConfigsRefuseAnotherAdminsClient(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, _ := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
	seedAdminWithInbound(t, "other", "other-tag", "theirs@example.com")
	db := database.GetDB()
	var actor model.User
	if err := db.First(&actor, resellerId).Error; err != nil {
		t.Fatalf("actor: %v", err)
	}
	idOf := func(email string) int {
		var rec model.ClientRecord
		if err := db.Where("email = ?", email).First(&rec).Error; err != nil {
			t.Fatalf("client %s: %v", email, err)
		}
		return rec.Id
	}
	recorder := &tunnelConfigRecorder{}
	service.RegisterSubLinkProvider(recorder)
	t.Cleanup(func() { service.RegisterSubLinkProvider(nil) })
	engine := newGuardedClientEngine(t, &actor)
	get := func(id int) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/panel/api/clients/tunnelConfigs/%d", id), nil))
		return rec
	}

	if rec := get(idOf("theirs@example.com")); rec.Code != http.StatusForbidden || len(recorder.emails) != 0 {
		t.Fatalf("foreign client: status %d, rendered for %v; want 403 and nothing rendered", rec.Code, recorder.emails)
	}

	rec := get(idOf("mine@example.com"))
	var resp struct {
		Success bool            `json:"success"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("own client: status %d body %s (%v)", rec.Code, rec.Body.String(), err)
	}
	// The browser parses an array; a null for a client with no tunnel would blank its window.
	if !resp.Success || string(resp.Obj) != "[]" || !reflect.DeepEqual(recorder.emails, []string{"mine@example.com"}) {
		t.Fatalf("own client: success %v obj %s rendered for %v", resp.Success, resp.Obj, recorder.emails)
	}
}

// The inbound window's links carry every client's keys, as the client window's do;
// a reseller whose super_admin switched clients.viewLinks off gets neither.
func TestInboundLinksNeedTheViewLinksSwitch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions string
		status      int
	}{
		{"switched off", `{"clients.viewLinks":false}`, http.StatusForbidden},
		{"role default", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
			resellerId, inboundId := seedAdminWithInbound(t, "res", "res-tag", "mine@example.com")
			db := database.GetDB()
			if err := db.Model(&model.User{}).Where("id = ?", resellerId).Update("permissions", tc.permissions).Error; err != nil {
				t.Fatalf("permissions: %v", err)
			}
			var actor model.User
			if err := db.First(&actor, resellerId).Error; err != nil {
				t.Fatalf("actor: %v", err)
			}
			recorder := &tunnelConfigRecorder{}
			service.RegisterSubLinkProvider(recorder)
			t.Cleanup(func() { service.RegisterSubLinkProvider(nil) })

			gin.SetMode(gin.TestMode)
			engine := gin.New()
			api := engine.Group("/panel/api", func(c *gin.Context) { session.SetAPIAuthUser(c, &actor) }, authz.Middleware(""))
			NewInboundController(api.Group("/inbounds"))
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/panel/api/inbounds/%d/links", inboundId), nil))

			rendered := len(recorder.inbounds) > 0
			if rec.Code != tc.status || rendered != (tc.status == http.StatusOK) {
				t.Fatalf("status %d, rendered %v; want %d (body %s)", rec.Code, rendered, tc.status, rec.Body.String())
			}
		})
	}
}
