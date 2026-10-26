package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

type hostOwnerFixture struct {
	boss, reseller          *model.User
	bossInbound, resInbound int
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
	inbounds := make([]int, 2)
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
		inbounds[i] = ib.Id
		for j, group := range owner.groups {
			h := model.Host{
				GroupId: group, InboundId: ib.Id, Remark: group, Address: group + ".example.com",
				SortOrder: 2*i + j, Tags: []string{"tag-" + group},
			}
			if err := db.Create(&h).Error; err != nil {
				t.Fatalf("host %s: %v", group, err)
			}
		}
	}
	return hostOwnerFixture{boss: &boss, reseller: reseller, bossInbound: inbounds[0], resInbound: inbounds[1]}
}

// seedMixedHostGroup adds an inbound the super_admin shares with the reseller and
// a super_admin group, mix-m, with one row there and then one on his own inbound.
func seedMixedHostGroup(t *testing.T, f hostOwnerFixture) (shared int) {
	t.Helper()
	db := database.GetDB()
	ib := model.Inbound{
		UserId: f.boss.Id, Tag: "hosts-owner-shared", Remark: "shared", Protocol: model.VLESS,
		Port: 40310, Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}",
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatalf("shared inbound: %v", err)
	}
	assign(t, f.reseller.Id, ib.Id)
	// The shared row first: an unscoped view builds a group from its first row.
	for _, inbound := range []int{ib.Id, f.bossInbound} {
		h := model.Host{
			GroupId: "mix-m", InboundId: inbound, Remark: "mix-m", Address: mixedAddress(inbound),
			SortOrder: 9, Tags: []string{"tag-mix-m"},
		}
		if err := db.Create(&h).Error; err != nil {
			t.Fatalf("mixed host: %v", err)
		}
	}
	return ib.Id
}

func mixedAddress(inbound int) string { return fmt.Sprintf("mix-%d.example.com", inbound) }

func seedHostManager(t *testing.T) *model.User {
	t.Helper()
	u := model.User{Username: "mgr-hosts", Password: "x", Role: model.RoleManager, Enabled: true}
	if err := database.GetDB().Create(&u).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	return &u
}

func hostCall(t *testing.T, engine *gin.Engine, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(engine, req)
	return rec.Code, rec.Body.String()
}

// hostRows lists the stored rows of one group, or of all with "", as
// "group inbound remark address on|off" in a fixed order.
func hostRows(t *testing.T, groupId string) []string {
	t.Helper()
	q := database.GetDB().Order("group_id, inbound_id, address")
	if groupId != "" {
		q = q.Where("group_id = ?", groupId)
	}
	var hosts []model.Host
	if err := q.Find(&hosts).Error; err != nil {
		t.Fatalf("hosts: %v", err)
	}
	rows := make([]string, len(hosts))
	for i, h := range hosts {
		rows[i] = hostRow(h.GroupId, h.InboundId, h.Remark, h.Address, !h.IsDisabled)
	}
	return rows
}

func hostRow(group string, inbound int, remark, address string, enabled bool) string {
	state := "on"
	if !enabled {
		state = "off"
	}
	return fmt.Sprintf("%s %d %s %s %s", group, inbound, remark, address, state)
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

func decodeHostObj(t *testing.T, code int, body string, into any) {
	t.Helper()
	var env hostEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil || code != http.StatusOK || !env.Success {
		t.Fatalf("want a successful answer, got %d %s", code, body)
	}
	if err := json.Unmarshal(env.Obj, into); err != nil {
		t.Fatalf("decode obj: %v in %s", err, body)
	}
}

// /get, /byInbound and /tags answered from every admin's rows, so a reseller who
// learned another admin's group id could read that admin's domains and SNI.
func TestHostReadRoutesHandAResellerOnlyRowsOnTheirInbounds(t *testing.T) {
	f := seedHostOwners(t)
	shared := seedMixedHostGroup(t, f)
	engine := newHostOwnerEngine(f.reseller)
	get := func(path string) (int, string) {
		return hostCall(t, engine, http.MethodGet, "/panel/api/hosts"+path, "")
	}

	t.Run("get", func(t *testing.T) {
		_, missing := get("/get/no-such-group")
		for _, foreign := range []string{"boss-a", "boss-b"} {
			if code, body := get("/get/" + foreign); code != http.StatusOK || body != missing {
				t.Fatalf("get/%s = %d %s, want the answer for a missing group: %s", foreign, code, body, missing)
			}
		}
		var own entity.HostGroup
		code, body := get("/get/res-x")
		decodeHostObj(t, code, body, &own)
		if own.GroupId != "res-x" || !slices.Equal(own.Hosts, []string{"res-x.example.com"}) {
			t.Fatalf("get/res-x = %+v", own)
		}
		var mixed entity.HostGroup
		code, body = get("/get/mix-m")
		decodeHostObj(t, code, body, &mixed)
		if !slices.Equal(mixed.InboundIds, []int{shared}) || !slices.Equal(mixed.Hosts, []string{mixedAddress(shared)}) {
			t.Fatalf("get/mix-m handed the reseller rows off their inbounds: %s", body)
		}
	})

	t.Run("byInbound", func(t *testing.T) {
		var groups []entity.HostGroup
		code, body := get("/byInbound/" + strconv.Itoa(shared))
		decodeHostObj(t, code, body, &groups)
		if len(groups) != 1 || groups[0].GroupId != "mix-m" ||
			!slices.Equal(groups[0].InboundIds, []int{shared}) || !slices.Equal(groups[0].Hosts, []string{mixedAddress(shared)}) {
			t.Fatalf("byInbound on the shared inbound handed the reseller rows off their inbounds: %s", body)
		}
		groups = nil
		code, body = get("/byInbound/" + strconv.Itoa(f.bossInbound))
		decodeHostObj(t, code, body, &groups)
		if len(groups) != 0 {
			t.Fatalf("byInbound on the super admin's inbound answered the reseller: %s", body)
		}
	})

	t.Run("tags", func(t *testing.T) {
		var tags []string
		code, body := get("/tags")
		decodeHostObj(t, code, body, &tags)
		if want := []string{"tag-mix-m", "tag-res-x", "tag-res-y"}; !slices.Equal(tags, want) {
			t.Fatalf("tags = %v, want %v", tags, want)
		}
	})
}

// Managers and the super_admin oversee every admin: each read answers them byte
// for byte what the unscoped service returns, mixed groups whole.
func TestHostReadRoutesStayUnscopedForSuperAdminAndManager(t *testing.T) {
	f := seedHostOwners(t)
	shared := seedMixedHostGroup(t, f)
	manager := seedHostManager(t)
	svc := service.HostService{}
	answer := func(obj any, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("unscoped service: %v", err)
		}
		b, err := json.Marshal(entity.Msg{Success: true, Obj: obj})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	want := map[string]string{
		"/list":       answer(svc.GetHosts()),
		"/get/boss-a": answer(svc.GetHostGroup("boss-a")),
		"/get/res-x":  answer(svc.GetHostGroup("res-x")),
		"/get/mix-m":  answer(svc.GetHostGroup("mix-m")),
		"/tags":       answer(svc.GetAllTags()),
	}
	for _, inbound := range []int{f.bossInbound, f.resInbound, shared} {
		want["/byInbound/"+strconv.Itoa(inbound)] = answer(svc.GetHostsByInbound(inbound))
	}
	if both := fmt.Sprintf(`"inboundIds":[%d,%d]`, shared, f.bossInbound); !strings.Contains(want["/get/mix-m"], both) {
		t.Fatalf("fixture: the unscoped mixed group lacks %s: %s", both, want["/get/mix-m"])
	}
	for name, actor := range map[string]*model.User{"super admin": f.boss, "manager": manager} {
		engine := newHostOwnerEngine(actor)
		for path, body := range want {
			if code, got := hostCall(t, engine, http.MethodGet, "/panel/api/hosts"+path, ""); code != http.StatusOK || got != body {
				t.Fatalf("%s %s = %d %s, want %s", name, path, code, got, body)
			}
		}
	}
}

// hostWriteEnv is a fresh fixture, mixed group included, with an engine that
// acts as one admin of the given role.
type hostWriteEnv struct {
	f      hostOwnerFixture
	shared int
	engine *gin.Engine
}

func newHostWriteEnv(t *testing.T, role model.Role) hostWriteEnv {
	t.Helper()
	f := seedHostOwners(t)
	shared := seedMixedHostGroup(t, f)
	actor := f.boss
	switch role {
	case model.RoleReseller:
		actor = f.reseller
	case model.RoleManager:
		actor = seedHostManager(t)
	}
	return hostWriteEnv{f: f, shared: shared, engine: newHostOwnerEngine(actor)}
}

// body fills BOSS, RES and SHARED in a JSON body with the fixture's inbound ids.
func (e hostWriteEnv) body(s string) string {
	return strings.NewReplacer(
		"BOSS", strconv.Itoa(e.f.bossInbound), "RES", strconv.Itoa(e.f.resInbound), "SHARED", strconv.Itoa(e.shared),
	).Replace(s)
}

func (e hostWriteEnv) refused(t *testing.T, path, body string, wantCode int, wantMsg string) {
	t.Helper()
	before := hostRows(t, "")
	code, got := hostCall(t, e.engine, http.MethodPost, "/panel/api/hosts"+path, e.body(body))
	if code != wantCode || !strings.Contains(got, `"success":false`) || !strings.Contains(got, wantMsg) {
		t.Fatalf("%s = %d %s, want %d refusing with %q", path, code, got, wantCode, wantMsg)
	}
	if after := hostRows(t, ""); !slices.Equal(after, before) {
		t.Fatalf("refused %s changed rows:\n%v\nwant\n%v", path, after, before)
	}
}

func (e hostWriteEnv) ok(t *testing.T, path, body string) string {
	t.Helper()
	code, got := hostCall(t, e.engine, http.MethodPost, "/panel/api/hosts"+path, e.body(body))
	if code != http.StatusOK || !strings.Contains(got, `"success":true`) {
		t.Fatalf("%s = %d %s, want success", path, code, got)
	}
	return got
}

func wantHostRows(t *testing.T, groupId string, want ...string) {
	t.Helper()
	if got := hostRows(t, groupId); !slices.Equal(got, want) {
		t.Fatalf("rows of %s:\n%v\nwant\n%v", groupId, got, want)
	}
}

// wantDetachedRow checks the one row on the shared inbound, which the reseller's
// write moved out of mix-m into a group of its own, and returns that group's id.
func wantDetachedRow(t *testing.T, shared int, remark, address string, enabled bool) string {
	t.Helper()
	var rows []model.Host
	if err := database.GetDB().Where("inbound_id = ?", shared).Find(&rows).Error; err != nil {
		t.Fatalf("hosts on %d: %v", shared, err)
	}
	if len(rows) != 1 || rows[0].GroupId == "" || rows[0].GroupId == "mix-m" {
		t.Fatalf("rows on the shared inbound = %+v, want one outside mix-m", rows)
	}
	h := rows[0]
	if got, want := hostRow(h.GroupId, h.InboundId, h.Remark, h.Address, !h.IsDisabled), hostRow(h.GroupId, shared, remark, address, enabled); got != want {
		t.Fatalf("detached row = %s, want %s", got, want)
	}
	return h.GroupId
}

// bossMixedRow is the super_admin's own row in mix-m as stored, its save time aside.
func bossMixedRow(t *testing.T, e hostWriteEnv) string {
	t.Helper()
	var h model.Host
	if err := database.GetDB().Where("group_id = ? AND inbound_id = ?", "mix-m", e.f.bossInbound).First(&h).Error; err != nil {
		t.Fatalf("his row in mix-m: %v", err)
	}
	h.UpdatedAt = 0
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// A reseller's write left their row in the super admin's group; his editor reads a group
// off its first row, so his next save copied their relay and allowInsecure onto his own.
func TestResellerHostWritesNeverReachTheSuperAdminsRowThroughAMixedGroup(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"update", "/update/mix-m", `{"inboundIds":[SHARED],"remark":"mix-m","hosts":["relay.r.net:443"],"allowInsecure":true,"sortOrder":-100}`},
		{"disable", "/setEnable/mix-m", `{"enable":false}`},
		{"bulk disable", "/bulk/setEnable", `{"ids":["mix-m"],"enable":false}`},
		{"reorder", "/reorder", `{"ids":["res-y","mix-m","res-x"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHostWriteEnv(t, model.RoleReseller)
			before := bossMixedRow(t, e)
			e.ok(t, tc.path, tc.body)

			svc := service.HostService{}
			group, err := svc.GetHostGroup("mix-m")
			if err != nil {
				t.Fatalf("his editor opens mix-m: %v", err)
			}
			if _, err := svc.UpdateHostGroup("mix-m", group); err != nil {
				t.Fatalf("his save of mix-m: %v", err)
			}
			if got := bossMixedRow(t, e); got != before {
				t.Fatalf("his save after the reseller's %s rewrote his own row:\n got %s\nwant %s", tc.name, got, before)
			}
		})
	}
}

// Every host write took a bare group id, so a reseller who learned another
// admin's id could rewrite or delete that admin's rows, or plant rows on theirs.
func TestHostWriteRoutesLetAResellerTouchOnlyRowsOnTheirInbounds(t *testing.T) {
	const notFound = "host group not found"
	reseller := func(t *testing.T) hostWriteEnv { return newHostWriteEnv(t, model.RoleReseller) }

	t.Run("update another admin's group", func(t *testing.T) {
		e := reseller(t)
		_, missing := hostCall(t, e.engine, http.MethodPost, "/panel/api/hosts/update/no-such-group", e.body(`{"inboundIds":[RES],"remark":"x"}`))
		if !strings.Contains(missing, "host not found") {
			t.Fatalf("update of a missing group = %s", missing)
		}
		e.refused(t, "/update/boss-a", `{"inboundIds":[RES],"remark":"hijack","hosts":["evil.example.com"]}`, http.StatusOK, "host not found")
	})
	t.Run("update onto another admin's inbound", func(t *testing.T) {
		reseller(t).refused(t, "/update/res-x", `{"inboundIds":[RES,BOSS],"remark":"res-x","hosts":["res-x.example.com"]}`, http.StatusForbidden, foreignMsg)
	})
	t.Run("update the shared part of a mixed group", func(t *testing.T) {
		e := reseller(t)
		answer := e.ok(t, "/update/mix-m", `{"inboundIds":[SHARED],"remark":"mix-edit","hosts":["mix-new.example.com"]}`)
		wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "mix-m", mixedAddress(e.f.bossInbound), true))
		detached := wantDetachedRow(t, e.shared, "mix-edit", "mix-new.example.com", true)
		var rows []model.Host
		decodeHostObj(t, http.StatusOK, answer, &rows)
		if len(rows) != 1 || rows[0].GroupId != detached {
			t.Fatalf("update answered %s, want the row under its new group %s", answer, detached)
		}
	})
	t.Run("delete another admin's group", func(t *testing.T) {
		reseller(t).refused(t, "/del/boss-a", "", http.StatusOK, notFound)
	})
	t.Run("delete a mixed group", func(t *testing.T) {
		e := reseller(t)
		e.ok(t, "/del/mix-m", "")
		wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "mix-m", mixedAddress(e.f.bossInbound), true))
	})
	t.Run("disable another admin's group", func(t *testing.T) {
		reseller(t).refused(t, "/setEnable/boss-a", `{"enable":false}`, http.StatusOK, notFound)
	})
	t.Run("disable a mixed group", func(t *testing.T) {
		e := reseller(t)
		e.ok(t, "/setEnable/mix-m", `{"enable":false}`)
		wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "mix-m", mixedAddress(e.f.bossInbound), true))
		wantDetachedRow(t, e.shared, "mix-m", mixedAddress(e.shared), false)
	})
	t.Run("bulk disable", func(t *testing.T) {
		e := reseller(t)
		e.refused(t, "/bulk/setEnable", `{"ids":["res-x","boss-a"],"enable":false}`, http.StatusOK, notFound)
		e.ok(t, "/bulk/setEnable", `{"ids":["res-x","mix-m"],"enable":false}`)
		wantHostRows(t, "res-x", hostRow("res-x", e.f.resInbound, "res-x", "res-x.example.com", false))
		wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "mix-m", mixedAddress(e.f.bossInbound), true))
		wantDetachedRow(t, e.shared, "mix-m", mixedAddress(e.shared), false)
	})
	t.Run("bulk disable two mixed groups", func(t *testing.T) {
		e := reseller(t)
		for _, inbound := range []int{e.shared, e.f.bossInbound} {
			if err := database.GetDB().Create(&model.Host{GroupId: "mix-n", InboundId: inbound, Remark: "mix-n", Address: "n.example.com"}).Error; err != nil {
				t.Fatalf("mix-n: %v", err)
			}
		}
		e.ok(t, "/bulk/setEnable", `{"ids":["mix-m","mix-n"],"enable":false}`)
		var groups []string
		if err := database.GetDB().Model(&model.Host{}).Where("inbound_id = ?", e.shared).Distinct().Pluck("group_id", &groups).Error; err != nil {
			t.Fatalf("groups: %v", err)
		}
		if len(groups) != 2 || slices.Contains(groups, "mix-m") || slices.Contains(groups, "mix-n") {
			t.Fatalf("groups on the shared inbound = %v, want two new ones, one per split group", groups)
		}
	})
	t.Run("bulk delete", func(t *testing.T) {
		e := reseller(t)
		e.refused(t, "/bulk/del", `{"ids":["res-y","boss-b"]}`, http.StatusOK, notFound)
		e.ok(t, "/bulk/del", `{"ids":["res-y","mix-m"]}`)
		wantHostRows(t, "res-y")
		wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "mix-m", mixedAddress(e.f.bossInbound), true))
	})
	t.Run("add onto another admin's inbound", func(t *testing.T) {
		e := reseller(t)
		for _, path := range []string{"/add", "/bulk/add"} {
			e.refused(t, path, `{"inboundIds":[RES,BOSS],"remark":"planted","hosts":["planted.example.com"]}`, http.StatusForbidden, foreignMsg)
		}
	})
	t.Run("add into another admin's group", func(t *testing.T) {
		reseller(t).refused(t, "/add", `{"groupId":"boss-a","inboundIds":[RES],"remark":"planted","hosts":["planted.example.com"]}`, http.StatusOK, "host group ID is already in use")
	})
	t.Run("add into a mixed group", func(t *testing.T) {
		reseller(t).refused(t, "/add", `{"groupId":"mix-m","inboundIds":[RES],"remark":"planted","hosts":["planted.example.com"]}`, http.StatusOK, "host group ID is already in use")
	})
	t.Run("add on their own inbounds", func(t *testing.T) {
		e := reseller(t)
		e.ok(t, "/bulk/add", `{"groupId":"res-x","inboundIds":[RES],"remark":"res-x","hosts":["res-x2.example.com"]}`)
		wantHostRows(t, "res-x",
			hostRow("res-x", e.f.resInbound, "res-x", "res-x.example.com", true),
			hostRow("res-x", e.f.resInbound, "res-x", "res-x2.example.com", true))
		e.ok(t, "/add", `{"groupId":"res-new","inboundIds":[RES,SHARED],"remark":"res-new","hosts":["new.example.com"]}`)
		wantHostRows(t, "res-new",
			hostRow("res-new", e.f.resInbound, "res-new", "new.example.com", true),
			hostRow("res-new", e.shared, "res-new", "new.example.com", true))
	})
}

// The scoping must not reach managers or the super_admin: they still write any
// admin's group whole, mixed groups included, and unknown bulk ids pass as before.
func TestHostWriteRoutesStayUnscopedForSuperAdminAndManager(t *testing.T) {
	for _, role := range []model.Role{model.RoleSuperAdmin, model.RoleManager} {
		t.Run(string(role), func(t *testing.T) {
			e := newHostWriteEnv(t, role)
			e.ok(t, "/update/mix-m", `{"inboundIds":[BOSS],"remark":"whole","hosts":["whole.example.com"]}`)
			wantHostRows(t, "mix-m", hostRow("mix-m", e.f.bossInbound, "whole", "whole.example.com", true))
			e.ok(t, "/setEnable/res-x", `{"enable":false}`)
			wantHostRows(t, "res-x", hostRow("res-x", e.f.resInbound, "res-x", "res-x.example.com", false))
			e.ok(t, "/bulk/setEnable", `{"ids":["res-y","no-such-group"],"enable":false}`)
			wantHostRows(t, "res-y", hostRow("res-y", e.f.resInbound, "res-y", "res-y.example.com", false))
			e.ok(t, "/add", `{"groupId":"res-y","inboundIds":[BOSS],"remark":"res-y","hosts":["boss-side.example.com"]}`)
			wantHostRows(t, "res-y",
				hostRow("res-y", e.f.bossInbound, "res-y", "boss-side.example.com", true),
				hostRow("res-y", e.f.resInbound, "res-y", "res-y.example.com", false))
			e.ok(t, "/del/boss-a", "")
			wantHostRows(t, "boss-a")
			e.ok(t, "/bulk/del", `{"ids":["res-x","no-such-group"]}`)
			wantHostRows(t, "res-x")
		})
	}
}
