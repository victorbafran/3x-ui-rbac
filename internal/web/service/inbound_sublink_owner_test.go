package service

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// recordingLinkProvider answers with one link per (inbound, email) it is asked
// for. The scoping decision is which pairs it is asked for at all, so that is
// what the test reads back; the real provider's link format is not under test.
type recordingLinkProvider struct{ asked []string }

func (p *recordingLinkProvider) SubLinksForSubId(string, string) ([]string, error) { return nil, nil }

func (p *recordingLinkProvider) LinksForClient(_ string, inbound *model.Inbound, email string) []string {
	p.asked = append(p.asked, fmt.Sprintf("%d/%s", inbound.Id, email))
	return []string{fmt.Sprintf("link:%d:%s", inbound.Id, email)}
}

func (p *recordingLinkProvider) LinksForInbounds(_ string, inbounds []*model.Inbound) []string {
	out := []string{}
	for _, ib := range inbounds {
		out = append(out, fmt.Sprintf("whole-inbound:%d", ib.Id))
	}
	return out
}

func attachClient(t *testing.T, adminId, inboundId int, email string) {
	t.Helper()
	db := database.GetDB()
	cr := model.ClientRecord{Email: email, Enable: true, AdminId: adminId}
	if err := db.Create(&cr).Error; err != nil {
		t.Fatalf("client %s: %v", email, err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: cr.Id, InboundId: inboundId}).Error; err != nil {
		t.Fatalf("attach %s: %v", email, err)
	}
}

// A subscription link is a working credential. On an inbound two admins share,
// listing every client on it handed a reseller the other admin's customers.
func TestAllInboundLinksCoverOnlyTheCallersClients(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	prev := registeredSubLinkProvider
	rec := &recordingLinkProvider{}
	RegisterSubLinkProvider(rec)
	t.Cleanup(func() { RegisterSubLinkProvider(prev) })

	db := database.GetDB()
	boss := model.User{Username: "boss-link", Password: "x", Role: model.RoleSuperAdmin, Enabled: true}
	if err := db.Create(&boss).Error; err != nil {
		t.Fatalf("boss: %v", err)
	}
	reseller := model.User{Username: "res-link", Password: "x", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(&reseller).Error; err != nil {
		t.Fatalf("reseller: %v", err)
	}
	shared := model.Inbound{UserId: boss.Id, Tag: "shared-link", Remark: "shared-link", Protocol: model.VLESS,
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(&shared).Error; err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if err := db.Create(&model.AdminInbound{AdminId: reseller.Id, InboundId: shared.Id}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}
	attachClient(t, boss.Id, shared.Id, "boss-customer@example.com")
	attachClient(t, reseller.Id, shared.Id, "a9:mine@example.com")

	svc := &InboundService{}
	links, err := svc.GetAllInboundLinksForOwner("example.com", reseller.Id)
	if err != nil {
		t.Fatalf("GetAllInboundLinksForOwner: %v", err)
	}
	want := fmt.Sprintf("link:%d:a9:mine@example.com", shared.Id)
	if len(links) != 1 || links[0] != want {
		t.Fatalf("reseller got links %v, want only [%s]", links, want)
	}
	for _, asked := range rec.asked {
		if asked == fmt.Sprintf("%d/boss-customer@example.com", shared.Id) {
			t.Fatalf("links were built for another admin's customer: %v", rec.asked)
		}
	}
}
