package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// An exit host sends customers out through another server, which is the relays
// switch's to decide: a manager runs hosts, but not the ones that leave elsewhere.
func TestOnlyTheRelaysSwitchChoosesWhereAHostLeaves(t *testing.T) {
	newHostTestDB(t)
	db := database.GetDB()
	var boss model.User
	if err := db.Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	manager := model.User{Username: "mgr-exits", Password: "x", Role: model.RoleManager, Enabled: true}
	ib := model.Inbound{Tag: "exit-guard", Protocol: model.VLESS, Port: 40400, Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}"}
	for _, row := range []any{
		&manager, &ib,
		&model.Relay{Id: 9, Name: "nl", Address: "198.51.100.9", KeyPath: "/k9", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
		&model.Relay{Id: 10, Name: "uk", Address: "198.51.100.10", KeyPath: "/k10", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	save := func(actor *model.User, path, remark, exit string) (int, string) {
		t.Helper()
		body, _ := json.Marshal(entity.HostGroup{Remark: remark, InboundIds: []int{ib.Id}, Hosts: []string{"tun.example.ir"}, Exit: exit})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		newHostOwnerEngine(actor).ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	stored := func(remark string) model.Host {
		t.Helper()
		var h model.Host
		if err := db.Where("remark = ?", remark).First(&h).Error; err != nil {
			t.Fatalf("host %s: %v", remark, err)
		}
		return h
	}

	if code, body := save(&manager, "/panel/api/hosts/add", "mgr", "wgr9"); code != http.StatusForbidden || !strings.Contains(body, "sections.relays") {
		t.Errorf("a manager adding an exit: %d %s", code, body)
	}
	if code, body := save(&manager, "/panel/api/hosts/add", "mgr", ""); code != http.StatusOK || !strings.Contains(body, `"success":true`) {
		t.Errorf("a manager adding a host without an exit: %d %s", code, body)
	}
	if code, body := save(&boss, "/panel/api/hosts/add", "NL", "wgr9"); code != http.StatusOK || !strings.Contains(body, `"success":true`) {
		t.Fatalf("the super admin adding an exit: %d %s", code, body)
	}
	// Even with its exit as it was: the inbounds and route it saves decide whose
	// customers leave through that exit too.
	update := "/panel/api/hosts/update/" + stored("NL").GroupId
	if code, body := save(&manager, update, "NL renamed", "wgr9"); code != http.StatusForbidden || !strings.Contains(body, "sections.relays") {
		t.Errorf("a manager saving an exit host: %d %s", code, body)
	}
	if h := stored("NL"); h.Exit != "wgr9" {
		t.Errorf("the refused save left the host leaving through %q; want wgr9", h.Exit)
	}
}
