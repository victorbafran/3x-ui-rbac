package service

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	sharedViewSettings = `{"clients":[{"id":"boss-secret-uuid","email":"boss-customer@example.com","enable":true},{"id":"mine-uuid","email":"a9:mine@example.com","enable":true}],"decryption":"none"}`
	ownViewSettings    = `{"clients":[{"id":"own-uuid","email":"a9:own@example.com","enable":true}],"decryption":"none"}`
)

// seedSharedView is a super_admin's inbound shared with a reseller, one client
// each, plus an inbound of the reseller's own that carries only their client.
func seedSharedView(t *testing.T) (resellerId, sharedId, ownId int) {
	t.Helper()
	db := database.GetDB()
	boss := model.User{Username: "boss-view", Password: "x", Role: model.RoleSuperAdmin, Enabled: true}
	reseller := model.User{Username: "res-view", Password: "x", Role: model.RoleReseller, Enabled: true}
	for _, u := range []*model.User{&boss, &reseller} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("user %s: %v", u.Username, err)
		}
	}
	shared := model.Inbound{
		UserId: boss.Id, Tag: "shared-view", Remark: "shared-view", Protocol: model.VLESS, Port: 40101,
		Settings: sharedViewSettings, StreamSettings: "{}", Sniffing: "{}",
	}
	own := model.Inbound{
		UserId: reseller.Id, Tag: "own-view", Remark: "own-view", Protocol: model.VLESS, Port: 40102,
		Settings: ownViewSettings, StreamSettings: "{}", Sniffing: "{}",
	}
	for _, ib := range []*model.Inbound{&shared, &own} {
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("inbound %s: %v", ib.Tag, err)
		}
	}
	if err := db.Create(&model.AdminInbound{AdminId: reseller.Id, InboundId: shared.Id}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}
	for _, c := range []struct {
		admin, inbound int
		email          string
	}{
		{boss.Id, shared.Id, "boss-customer@example.com"},
		{reseller.Id, shared.Id, "a9:mine@example.com"},
		{reseller.Id, own.Id, "a9:own@example.com"},
	} {
		attachClient(t, c.admin, c.inbound, c.email)
		if err := db.Create(&xray.ClientTraffic{InboundId: c.inbound, Email: c.email, Enable: true}).Error; err != nil {
			t.Fatalf("traffic %s: %v", c.email, err)
		}
	}
	return reseller.Id, shared.Id, own.Id
}

func viewEmails(t *testing.T, ib *model.Inbound) (settings, stats []string) {
	t.Helper()
	var parsed struct {
		Clients []struct {
			Email string `json:"email"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatalf("inbound %d settings: %v", ib.Id, err)
	}
	for _, c := range parsed.Clients {
		settings = append(settings, c.Email)
	}
	for _, st := range ib.ClientStats {
		stats = append(stats, st.Email)
	}
	sort.Strings(settings)
	sort.Strings(stats)
	return settings, stats
}

func inboundById(t *testing.T, list []*model.Inbound, id int) *model.Inbound {
	t.Helper()
	for _, ib := range list {
		if ib.Id == id {
			return ib
		}
	}
	t.Fatalf("inbound %d missing from %d rows", id, len(list))
	return nil
}

// Settings carry every client's credentials. On an inbound two admins share, the
// list, slim list and detail reads handed a reseller the other admin's UUIDs.
func TestInboundReadsCarryOnlyTheCallersClients(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	resellerId, sharedId, ownId := seedSharedView(t)
	svc := &InboundService{}
	mine := []string{"a9:mine@example.com"}
	both := []string{"a9:mine@example.com", "boss-customer@example.com"}

	reads := map[string]func(ownerId int) *model.Inbound{
		"list": func(ownerId int) *model.Inbound {
			list, err := svc.GetInboundsForOwner(ownerId)
			if err != nil {
				t.Fatalf("GetInboundsForOwner: %v", err)
			}
			return inboundById(t, list, sharedId)
		},
		"slim": func(ownerId int) *model.Inbound {
			list, err := svc.GetInboundsSlimForOwner(ownerId)
			if err != nil {
				t.Fatalf("GetInboundsSlimForOwner: %v", err)
			}
			return inboundById(t, list, sharedId)
		},
		"detail": func(ownerId int) *model.Inbound {
			ib, err := svc.GetInboundDetailForOwner(sharedId, ownerId)
			if err != nil {
				t.Fatalf("GetInboundDetailForOwner: %v", err)
			}
			return ib
		},
	}
	for name, read := range reads {
		t.Run(name+" as the reseller", func(t *testing.T) {
			ib := read(resellerId)
			settings, stats := viewEmails(t, ib)
			if !reflect.DeepEqual(settings, mine) || !reflect.DeepEqual(stats, mine) {
				t.Fatalf("settings clients %v, stats %v; want only %v", settings, stats, mine)
			}
			if strings.Contains(ib.Settings, "boss-secret-uuid") {
				t.Fatalf("the other admin's UUID reached the reseller: %s", ib.Settings)
			}
		})
		t.Run(name+" unscoped", func(t *testing.T) {
			ib := read(0)
			settings, stats := viewEmails(t, ib)
			if !reflect.DeepEqual(settings, both) || !reflect.DeepEqual(stats, both) {
				t.Fatalf("settings clients %v, stats %v; want %v", settings, stats, both)
			}
			if name != "slim" && ib.Settings != sharedViewSettings {
				t.Fatalf("unscoped settings rewritten:\n got %s\nwant %s", ib.Settings, sharedViewSettings)
			}
		})
	}

	// An inbound holding only the caller's clients comes back exactly as stored.
	list, err := svc.GetInboundsForOwner(resellerId)
	if err != nil {
		t.Fatalf("GetInboundsForOwner: %v", err)
	}
	if got := inboundById(t, list, ownId).Settings; got != ownViewSettings {
		t.Fatalf("the reseller's own inbound was rewritten:\n got %s\nwant %s", got, ownViewSettings)
	}
}
