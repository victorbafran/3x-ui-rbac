package service

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func seedExitWorld(t *testing.T) map[string]int {
	t.Helper()
	setupRelayDB(t)
	seedRelays(t,
		model.Relay{Id: 9, Name: "nl", Address: "198.51.100.9", KeyPath: "/k9", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
		model.Relay{Id: 10, Name: "uk", Address: "198.51.100.10", KeyPath: "/k10", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
		model.Relay{Id: 8, Name: "iran", Address: "203.0.113.9", KeyPath: "/k8", Tunnel: "backpack", Role: model.RelayFront, Enabled: true},
	)
	node := 3
	ids := map[string]int{}
	for _, ib := range []*model.Inbound{
		{Tag: "vless", Port: 41330, Protocol: model.VLESS, Enable: true},
		{Tag: "vmess", Port: 2052, Protocol: model.VMESS, Enable: true},
		{Tag: "node-vless", Port: 2053, Protocol: model.VLESS, Enable: true, NodeID: &node},
		{Tag: "hysteria", Port: 8443, Protocol: model.Hysteria, Enable: true},
		{Tag: "wireguard", Port: 51820, Protocol: model.WireGuard, Enable: true},
		{Tag: "node-wireguard", Port: 51821, Protocol: model.WireGuard, Enable: true, NodeID: &node},
		{Tag: "amneziawg", Port: 51822, Protocol: model.AmneziaWG, Enable: true},
		{Tag: "tuic", Port: 8444, Protocol: model.TUIC, Enable: true},
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
		ids[ib.Tag] = ib.Id
	}
	seedHysteriaClients(t, ids["hysteria"], map[string]string{
		"hex@x":    "0123456789abcdef0123456789abcdef",
		"dashed@x": "01234567-89ab-4def-8123-456789abcdef",
	})
	return ids
}

// seedHysteriaClients attaches clients to an inbound in email order.
func seedHysteriaClients(t *testing.T, inboundId int, auths map[string]string) {
	t.Helper()
	db := database.GetDB()
	for _, email := range slices.Sorted(maps.Keys(auths)) {
		rec := &model.ClientRecord{Email: email, Auth: auths[email], Enable: true}
		if err := db.Create(rec).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: inboundId}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func exitHost(remark string, inbound int, exit, route string) *entity.HostGroup {
	return &entity.HostGroup{Remark: remark, InboundIds: []int{inbound}, Hosts: []string{"tun.example.ir"}, Exit: exit, VlessRoute: route}
}

// A country is a route picking an exit on a local VLESS or Hysteria inbound: the
// route is set from the exit, and the protocols that carry no route are refused.
func TestAHostExitIsACountryOnALocalVLESSOrHysteriaInbound(t *testing.T) {
	ids := seedExitWorld(t)
	s := &HostService{}

	for _, tag := range []string{"vless", "hysteria"} {
		created, err := s.AddHostGroup(exitHost("NL "+tag, ids[tag], "wgr9", ""))
		if err != nil {
			t.Fatalf("a %s host with an exit was refused: %v", tag, err)
		}
		if created[0].Exit != "wgr9" || created[0].VlessRoute != "9" {
			t.Errorf("%s: stored exit %q route %q; want wgr9 and the exit's id as the route", tag, created[0].Exit, created[0].VlessRoute)
		}
	}

	for _, tc := range []struct {
		name, want string
		req        *entity.HostGroup
	}{
		{"vmess carries no route", "needs a VLESS, Hysteria or WireGuard inbound", exitHost("x", ids["vmess"], "wgr9", "")},
		{"amneziawg is not taken", "AmneziaWG and TUIC cannot take one", exitHost("x", ids["amneziawg"], "wgr9", "")},
		{"tuic is not taken", "AmneziaWG and TUIC cannot take one", exitHost("x", ids["tuic"], "wgr9", "")},
		{"a node's traffic never reaches this server", "runs on a node", exitHost("x", ids["node-vless"], "wgr9", "")},
		{"nor a node's WireGuard", "runs on a node", exitHost("x", ids["node-wireguard"], "wgr9", "")},
		{"no such exit", "there is no exit server wgr5", exitHost("x", ids["vless"], "wgr5", "")},
		{"an entry server is not an exit", "there is no exit server wgr8", exitHost("x", ids["vless"], "wgr8", "")},
		{"a route out of range", "not a number from 0 to 65535", exitHost("x", ids["vless"], "wgr10", "70000")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.AddHostGroup(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// One route picks one exit on an inbound, VLESS or Hysteria; a second exit on it
// would be decided by rule order, silently. The same group may save itself again.
func TestOneRouteLeavesThroughOneExit(t *testing.T) {
	ids := seedExitWorld(t)
	s := &HostService{}
	nl, err := s.AddHostGroup(exitHost("NL", ids["vless"], "wgr9", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHostGroup(exitHost("UK", ids["vless"], "wgr10", "9")); err == nil || !strings.Contains(err.Error(), "already leaves through wgr9") {
		t.Errorf("route 9 to a second exit: got %v", err)
	}
	if _, err := s.UpdateHostGroup(nl[0].GroupId, exitHost("NL", ids["vless"], "wgr9", "9")); err != nil {
		t.Errorf("re-saving the host was refused as its own clash: %v", err)
	}

	if _, err := s.AddHostGroup(exitHost("UK hy", ids["hysteria"], "wgr10", "9")); err != nil {
		t.Errorf("route 9 on the Hysteria inbound was refused for the VLESS inbound's route 9: %v", err)
	}
	if _, err := s.AddHostGroup(exitHost("NL hy", ids["hysteria"], "wgr9", "9")); err == nil || !strings.Contains(err.Error(), "already leaves through wgr10") {
		t.Errorf("Hysteria route 9 to a second exit: got %v", err)
	}
}

// A Hysteria route rides the client's auth, so an exit there is refused while a
// client's auth is no UUID, naming the first few clients to regenerate.
func TestAHysteriaExitNeedsEveryAuthToBeAUUID(t *testing.T) {
	ids := seedExitWorld(t)
	seedHysteriaClients(t, ids["hysteria"], map[string]string{
		"a@x": "secret",
		"b@x": "{01234567-89ab-4def-8123-456789abcdef}",
		"c@x": "urn:uuid:01234567-89ab-4def-8123-456789abcdef",
		"d@x": "0123456789abcdef0123456789abcde",
		"e@x": "0123456789abcdef0123456789abcdeg",
		"f@x": "f",
		"g@x": "g",
	})
	_, err := (&HostService{}).AddHostGroup(exitHost("NL", ids["hysteria"], "wgr9", ""))
	want := "hysteria: regenerate the auth of a@x, b@x, c@x, d@x, e@x and 2 more"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want an error containing %q", err, want)
	}
	if strings.Contains(err.Error(), "hex@x") || strings.Contains(err.Error(), "dashed@x") {
		t.Errorf("%v names a client whose auth already is a UUID", err)
	}
}

// A WireGuard link carries no route, so an exit there takes the whole inbound:
// a second country on it is refused, the same country again is not.
func TestAWireGuardInboundLeavesThroughOneExit(t *testing.T) {
	ids := seedExitWorld(t)
	s := &HostService{}
	uk, err := s.AddHostGroup(exitHost("UK", ids["wireguard"], "wgr10", ""))
	if err != nil {
		t.Fatalf("a WireGuard host with an exit was refused: %v", err)
	}
	if uk[0].Exit != "wgr10" || uk[0].VlessRoute != "" {
		t.Errorf("stored exit %q route %q; want wgr10 and no route", uk[0].Exit, uk[0].VlessRoute)
	}
	if _, err := s.UpdateHostGroup(uk[0].GroupId, exitHost("NL", ids["wireguard"], "wgr9", "")); err != nil {
		t.Errorf("moving the inbound's only host to another country was refused as its own clash: %v", err)
	}
	_, err = s.AddHostGroup(exitHost("UK", ids["wireguard"], "wgr10", ""))
	if err == nil || !strings.Contains(err.Error(), "wireguard already leaves through wgr9") || !strings.Contains(err.Error(), "own inbound") {
		t.Errorf("a second country on the WireGuard inbound: got %v", err)
	}
	if _, err := s.AddHostGroup(exitHost("NL v6", ids["wireguard"], "wgr9", "")); err != nil {
		t.Errorf("a second address for the same country was refused: %v", err)
	}

	// This server is a country too: its host would be named for it while the
	// inbound's one rule sends that host's customers abroad.
	_, err = s.AddHostGroup(exitHost("Here", ids["wireguard"], "", ""))
	if err == nil || !strings.Contains(err.Error(), "wireguard already leaves through wgr9") || !strings.Contains(err.Error(), "own inbound") {
		t.Errorf("a host leaving from this server beside an exit: got %v", err)
	}
	here := &model.Inbound{Tag: "wireguard-here", Port: 51823, Protocol: model.WireGuard, Enable: true}
	if err := database.GetDB().Create(here).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHostGroup(exitHost("Here", here.Id, "", "")); err != nil {
		t.Fatalf("a WireGuard host without an exit was refused: %v", err)
	}
	if _, err := s.AddHostGroup(exitHost("Here v6", here.Id, "", "")); err != nil {
		t.Errorf("a second address leaving from this server was refused: %v", err)
	}
	_, err = s.AddHostGroup(exitHost("UK", here.Id, "wgr10", ""))
	if err == nil || !strings.Contains(err.Error(), "wireguard-here already leaves through this server") || !strings.Contains(err.Error(), "own inbound") {
		t.Errorf("an exit beside a host leaving from this server: got %v", err)
	}
}

// Adding into a group that exists only appends rows, so the group's own rows
// stay beside the new ones and must clash with them like any other's.
func TestAddingIntoAnExistingGroupMeetsItsOwnRows(t *testing.T) {
	ids := seedExitWorld(t)
	s := &HostService{}
	for _, tc := range []struct {
		tag, first, second, route, want string
	}{
		{"wireguard", "wgr10", "wgr9", "", "already leaves through wgr10"},
		{"vless", "wgr9", "wgr10", "9", "VLESS route 9 already leaves through wgr9"},
	} {
		group, err := s.AddHostGroup(exitHost("first", ids[tc.tag], tc.first, ""))
		if err != nil {
			t.Fatal(err)
		}
		into := exitHost("second", ids[tc.tag], tc.second, tc.route)
		into.GroupId = group[0].GroupId
		if _, err := s.AddHostGroup(into); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: a second exit added into the group: got %v, want %q", tc.tag, err, tc.want)
		}
	}
}

func TestEachCountryBecomesAnOutboundAndALastRule(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}`),
	}
	node := 3
	inbounds := []*model.Inbound{
		{Id: 1, Tag: "in-443", Protocol: model.VLESS, Enable: true},
		{Id: 2, Tag: "off", Protocol: model.VLESS, Enable: false},
		{Id: 3, Tag: "on-node", Protocol: model.VLESS, Enable: true, NodeID: &node},
	}
	hosts := []*model.Host{
		{InboundId: 1, Address: "a.example.ir", Exit: "wgr9", VlessRoute: "9"},
		{InboundId: 1, Address: "b.example.ir", Exit: "wgr9", VlessRoute: "9"},
		{InboundId: 1, Exit: "wgr10", VlessRoute: "10", IsDisabled: true},
		{InboundId: 2, Exit: "wgr10", VlessRoute: "10"},
		{InboundId: 3, Exit: "wgr10", VlessRoute: "10"},
	}
	injectExitRoutes(cfg, inbounds, hosts)

	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 3 {
		t.Fatalf("outbounds %v; want the two template ones and exit-wgr9 alone", outbounds)
	}
	exit := outbounds[2]
	settings, _ := exit["settings"].(map[string]any)
	if exit["tag"] != "exit-wgr9" || exit["sendThrough"] != "10.255.255.9" || settings["targetStrategy"] != "ForceIPv4" {
		t.Errorf("exit outbound %v", exit)
	}

	// Pinned byte for byte: a VLESS exit must not change as other protocols join.
	const wantRouting = `{"rules":[{"ip":["geoip:private"],"outboundTag":"blocked","type":"field"},{"inboundTag":["in-443"],"outboundTag":"exit-wgr9","type":"field","vlessRoute":"9"}]}`
	if got := string(cfg.RouterConfig); got != wantRouting {
		t.Errorf("routing\n got %s\nwant %s", got, wantRouting)
	}
}

// Hysteria routes like VLESS, by inbound and route; a WireGuard exit is one rule
// on the inbound's tag alone, whatever route its hosts carry. AmneziaWG gets none.
func TestHysteriaAndWireGuardExitsBecomeRules(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
	inbounds := []*model.Inbound{
		{Id: 4, Tag: "wg-in", Protocol: model.WireGuard, Enable: true},
		{Id: 5, Tag: "hy-in", Protocol: model.Hysteria, Enable: true},
		{Id: 6, Tag: "awg-in", Protocol: model.AmneziaWG, Enable: true},
	}
	hosts := []*model.Host{
		{InboundId: 4, Address: "a.example.ir", Exit: "wgr10"},
		{InboundId: 4, Address: "b.example.ir", Exit: "wgr10", VlessRoute: "10"},
		{InboundId: 5, Address: "c.example.ir", Exit: "wgr9", VlessRoute: "9"},
		{InboundId: 5, Address: "d.example.ir", Exit: "wgr10", VlessRoute: "10"},
		{InboundId: 6, Address: "e.example.ir", Exit: "wgr9", VlessRoute: "9"},
	}
	injectExitRoutes(cfg, inbounds, hosts)

	const wantRouting = `{"rules":[` +
		`{"inboundTag":["wg-in"],"outboundTag":"exit-wgr10","type":"field"},` +
		`{"inboundTag":["hy-in"],"outboundTag":"exit-wgr9","type":"field","vlessRoute":"9"},` +
		`{"inboundTag":["hy-in"],"outboundTag":"exit-wgr10","type":"field","vlessRoute":"10"}]}`
	if got := string(cfg.RouterConfig); got != wantRouting {
		t.Errorf("routing\n got %s\nwant %s", got, wantRouting)
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 3 || outbounds[1]["tag"] != "exit-wgr10" || outbounds[2]["tag"] != "exit-wgr9" {
		t.Errorf("outbounds %v; want direct, exit-wgr10, exit-wgr9", outbounds)
	}
}

// Deleting an exit still in use is refused, whichever protocol uses it: an
// OpenVPN inbound, a cisco country before its first client, or a host.
func TestAnExitInUseByAnyProtocolCannotBeDeleted(t *testing.T) {
	ids := seedExitWorld(t)
	for _, ib := range []*model.Inbound{
		{Tag: "ovpn", Port: 1194, Protocol: model.OpenVPN, Enable: true, Settings: `{"exit":"wgr10"}`},
		{Tag: "cisco", Port: 443, Protocol: model.Cisco, Enable: true, Settings: `{"clients":[],"locations":[{"name":"uk","exit":"wgr10"}]}`},
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&HostService{}).AddHostGroup(exitHost("NL", ids["vless"], "wgr9", "")); err != nil {
		t.Fatal(err)
	}
	usage := (&RelayService{}).usage()
	if got := strings.Join(usage["wgr10"], ","); got != "ovpn,cisco" {
		t.Errorf("wgr10 used by %q; want the OpenVPN and the cisco inbound", got)
	}
	if err := (&RelayService{}).Delete(9); err == nil || !strings.Contains(err.Error(), "host NL") {
		t.Errorf("deleting an exit a host uses: got %v", err)
	}
}

// Once a Hysteria inbound has an exit, a client written there with an auth no
// route fits in would be named for the country and leave from this server.
func TestAHysteriaExitInboundTakesOnlyRoutableAuths(t *testing.T) {
	setupBulkDB(t)
	svc, inboundSvc := &ClientService{}, &InboundService{}
	plain := mkInbound(t, 23001, model.Hysteria, `{"clients":[]}`)
	routed := mkInbound(t, 23002, model.Hysteria, `{"clients":[]}`)
	const routable = "0123456789abcdef0123456789abcdef"
	add := func(email, auth string, inboundIds ...int) error {
		_, err := svc.Create(inboundSvc, &ClientCreatePayload{
			Client: model.Client{Email: email, Auth: auth, Enable: true}, InboundIds: inboundIds,
		})
		return err
	}
	for email, auth := range map[string]string{"legacy@x": "secret", "uuid@x": routable} {
		if err := add(email, auth, plain.Id, routed.Id); err != nil {
			t.Fatalf("%s before any exit: %v", email, err)
		}
	}
	if err := add("short@x", "secret2", plain.Id); err != nil {
		t.Fatalf("a short auth on an inbound with no exit was refused: %v", err)
	}
	exit := model.Host{GroupId: "nl", InboundId: routed.Id, Remark: "NL", Address: "tun.example.ir", Exit: "wgr9", VlessRoute: "9"}
	if err := database.GetDB().Create(&exit).Error; err != nil {
		t.Fatal(err)
	}

	want := routed.Tag + " has an exit country"
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", what, err, want)
		}
	}
	refused("a new client", add("new@x", "secret3", plain.Id, routed.Id))
	var written int64
	database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "new@x").Count(&written)
	if written != 0 {
		t.Errorf("the refused client was written to the inbound without an exit")
	}
	_, err := svc.AttachByEmail(inboundSvc, "short@x", []int{routed.Id})
	refused("attaching a short-auth client", err)
	_, err = svc.UpdateByEmail(inboundSvc, "uuid@x", model.Client{Email: "uuid@x", Auth: "secret4", Enable: true}, 0)
	refused("an edit to a short auth", err)
	if got := lookupClientRecord(t, "uuid@x").Auth; got != routable {
		t.Errorf("the refused edit stored auth %q", got)
	}
	if _, err := svc.UpdateByEmail(inboundSvc, "legacy@x", model.Client{Email: "legacy@x", TotalGB: 5, Enable: true}, 0); err != nil {
		t.Errorf("an edit leaving a stored auth as it was refused: %v", err)
	}
}
