package service

import (
	"encoding/json"
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
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
		ids[ib.Tag] = ib.Id
	}
	return ids
}

func exitHost(remark string, inbound int, exit, route string) *entity.HostGroup {
	return &entity.HostGroup{Remark: remark, InboundIds: []int{inbound}, Hosts: []string{"tun.example.ir"}, Exit: exit, VlessRoute: route}
}

// A country is a VLESS route picking an exit: the route is set from the exit,
// and only a local VLESS inbound can carry one.
func TestAHostExitIsACountryOnlyOnALocalVLESSInbound(t *testing.T) {
	ids := seedExitWorld(t)
	s := &HostService{}

	created, err := s.AddHostGroup(exitHost("NL", ids["vless"], "wgr9", ""))
	if err != nil {
		t.Fatalf("a VLESS host with an exit was refused: %v", err)
	}
	if created[0].Exit != "wgr9" || created[0].VlessRoute != "9" {
		t.Errorf("stored exit %q route %q; want wgr9 and the exit's id as the route", created[0].Exit, created[0].VlessRoute)
	}

	for _, tc := range []struct {
		name, want string
		req        *entity.HostGroup
	}{
		{"vmess cannot pick a route by UUID", "needs a VLESS inbound", exitHost("x", ids["vmess"], "wgr9", "")},
		{"a node's traffic never reaches this server", "runs on a node", exitHost("x", ids["node-vless"], "wgr9", "")},
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

// One route picks one exit on an inbound; a second exit on it would be
// decided by rule order, silently. The same group may save itself again.
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

	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	if len(routing.Rules) != 2 || routing.Rules[0]["outboundTag"] != "blocked" {
		t.Fatalf("rules %v; want the private block first and one exit rule after it", routing.Rules)
	}
	last := routing.Rules[1]
	tags, _ := last["inboundTag"].([]any)
	if len(tags) != 1 || tags[0] != "in-443" || last["vlessRoute"] != "9" || last["outboundTag"] != "exit-wgr9" {
		t.Errorf("exit rule %v", last)
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
