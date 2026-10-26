package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

type hostOwnerFixture struct {
	boss, reseller *model.User
}

// seedHostOwners gives the super_admin two host groups (boss-a, boss-b) and a
// reseller two (res-x, res-y), each on an inbound of its own admin.
func seedHostOwners(t *testing.T) hostOwnerFixture {
	t.Helper()
	newHostTestDB(t)
	db := database.GetDB()
	var boss model.User
	if err := db.Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	reseller := loadActor(t, seedReseller(t, "res-hosts"))
	for i, owner := range []struct {
		admin  int
		groups []string
	}{{boss.Id, []string{"boss-a", "boss-b"}}, {reseller.Id, []string{"res-x", "res-y"}}} {
		ib := model.Inbound{
			UserId: owner.admin, Tag: "hosts-owner-" + owner.groups[0], Remark: owner.groups[0], Protocol: model.VLESS,
			Port: 40300 + i, Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}",
		}
		if err := db.Create(&ib).Error; err != nil {
			t.Fatalf("inbound: %v", err)
		}
		for j, group := range owner.groups {
			h := model.Host{GroupId: group, InboundId: ib.Id, Remark: group, Address: group + ".example.com", SortOrder: 2*i + j}
			if err := db.Create(&h).Error; err != nil {
				t.Fatalf("host %s: %v", group, err)
			}
		}
	}
	return hostOwnerFixture{boss: &boss, reseller: reseller}
}

func newHostOwnerEngine(actor *model.User) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authz.ActorKey, actor)
		session.SetAPIAuthUser(c, actor)
		c.Next()
	})
	NewHostController(engine.Group("/panel/api/hosts"))
	return engine
}

func hostSortOrders(t *testing.T) map[string]int {
	t.Helper()
	var hosts []model.Host
	if err := database.GetDB().Find(&hosts).Error; err != nil {
		t.Fatalf("hosts: %v", err)
	}
	orders := make(map[string]int, len(hosts))
	for _, h := range hosts {
		orders[h.GroupId] = h.SortOrder
	}
	return orders
}

// The client form's host picker reads /hosts/raw. Unscoped, it handed a reseller
// every admin's host rows and the group ids the host write routes take.
func TestRawHostsHandAResellerOnlyHostsOnTheirInbounds(t *testing.T) {
	f := seedHostOwners(t)
	for _, tc := range []struct {
		name    string
		actor   *model.User
		want    []string
		unwants []string
	}{
		{"reseller", f.reseller, []string{"res-x", "res-y"}, []string{"boss-a", "boss-b"}},
		{"super admin", f.boss, []string{"boss-a", "boss-b", "res-x", "res-y"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(newHostOwnerEngine(tc.actor), httptest.NewRequest(http.MethodGet, "/panel/api/hosts/raw", nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `"success":true`) {
				t.Fatalf("raw: %d %s", rec.Code, body)
			}
			for _, group := range tc.want {
				if !strings.Contains(body, `"groupId":"`+group+`"`) {
					t.Fatalf("raw is missing %s: %s", group, body)
				}
			}
			for _, group := range tc.unwants {
				if strings.Contains(body, group) {
					t.Fatalf("raw handed the %s another admin's host %s: %s", tc.name, group, body)
				}
			}
		})
	}
}

// Upstream's reorder renumbers every group the request leaves out. A reseller's
// page names only their own, so that rewrote the other admins' rows too.
func TestHostReorderByAResellerLeavesOtherAdminsGroupsAlone(t *testing.T) {
	f := seedHostOwners(t)
	engine := newHostOwnerEngine(f.reseller)
	post := func(body string) string {
		req := httptest.NewRequest(http.MethodPost, "/panel/api/hosts/reorder", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return serve(engine, req).Body.String()
	}

	if body := post(`{"ids":["res-y"]}`); !strings.Contains(body, `"success":true`) {
		t.Fatalf("reorder of the reseller's own group failed: %s", body)
	}
	want := map[string]int{"boss-a": 0, "boss-b": 1, "res-y": 0, "res-x": 1}
	if got := hostSortOrders(t); len(got) != len(want) || got["boss-a"] != 0 || got["boss-b"] != 1 || got["res-y"] != 0 || got["res-x"] != 1 {
		t.Fatalf("sort orders after the reseller's reorder = %v, want %v", got, want)
	}

	if body := post(`{"ids":["boss-b","res-x"]}`); !strings.Contains(body, `"success":false`) {
		t.Fatalf("a reseller reordered the super admin's group: %s", body)
	}
	if got := hostSortOrders(t); got["boss-b"] != 1 || got["res-x"] != 1 {
		t.Fatalf("a refused reorder changed rows: %v", got)
	}
}
