package service

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func ciscoSettings(exits ...string) string {
	var locs []string
	for i, exit := range exits {
		locs = append(locs, `{"name":"c`+string(rune('a'+i))+`","exit":"`+exit+`"}`)
	}
	return `{"clients":[{"id":"7c0c3c4e-3d47-4f4b-9f3a-0d0c1b2a3e4f","email":"c@x","password":"p","enable":true}],"locations":[` + strings.Join(locs, ",") + `]}`
}

func tunInbounds(cfg *xray.Config) []string {
	var out []string
	for _, ib := range cfg.InboundConfigs {
		if ib.Protocol == "tun" {
			raw, _ := json.Marshal(ib)
			out = append(out, string(raw))
		}
	}
	return out
}

func sidecarExitInbounds() []*model.Inbound {
	node := 3
	return []*model.Inbound{
		{Id: 20, Tag: "cisco", Protocol: model.Cisco, Enable: true, Settings: ciscoSettings("psi-AT", "wgr9", "")},
		{Id: 21, Tag: "ovpn-at", Protocol: model.OpenVPN, Enable: true, Settings: `{"exit":"psi-AT"}`},
		{Id: 22, Tag: "ovpn-fr", Protocol: model.OpenVPN, Enable: true, Settings: `{"exit":"psi-FR"}`},
		{Id: 23, Tag: "cisco-off", Protocol: model.Cisco, Enable: false, Settings: ciscoSettings("psi-US")},
		{Id: 24, Tag: "cisco-node", Protocol: model.Cisco, Enable: true, NodeID: &node, Settings: ciscoSettings("psi-DE")},
	}
}

const psiphonTunAT = `{"listen":"0.0.0.0","port":0,"protocol":"tun","settings":{"name":"psi-AT","MTU":1500},` +
	`"tag":"tun-psi-AT","sniffing":{"enabled":true,"destOverride":["http","tls"],"routeOnly":false}}`

// A cisco location or OpenVPN inbound on psi-AT is routed by the sidecar into a device
// called psi-AT: one TUN per country in use, only while the inbound and country are on.
func TestACiscoOrOpenVPNPsiphonCountryGetsItsOwnTun(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
	// No host leaves through Psiphon at all: the TUN alone must still get its rules.
	psi := psiphonExits{socks: map[string]int{"AT": 11181, "US": 11183, "DE": 11184}, tuns: true}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psi)

	if got := tunInbounds(cfg); len(got) != 1 || got[0] != psiphonTunAT {
		t.Fatalf("TUN inbounds %q\nwant only %s: none for a disabled country, a disabled inbound or a node", got, psiphonTunAT)
	}
	const wantRouting = `{"rules":[` +
		`{"inboundTag":["tun-psi-AT"],"network":"udp","outboundTag":"direct","port":"53","type":"field"},` +
		`{"inboundTag":["tun-psi-AT"],"network":"udp","outboundTag":"blocked","type":"field"},` +
		`{"inboundTag":["tun-psi-AT"],"outboundTag":"exit-psi-AT","type":"field"}]}`
	if got := string(cfg.RouterConfig); got != wantRouting {
		t.Errorf("routing\n got %s\nwant %s", got, wantRouting)
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, o := range outbounds {
		tags = append(tags, o["tag"].(string))
	}
	if strings.Join(tags, " ") != "direct exit-psi-AT blocked" {
		t.Errorf("outbounds %v; want direct, the AT SOCKS outbound and blocked", tags)
	}
}

// One TUN the core cannot open aborts its whole start, every Xray inbound with it;
// without CAP_NET_ADMIN or /dev/net/tun the pools stay held at their blackhole.
func TestNoTunWhereTheCoreCannotOpenOne(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181}})
	if got := tunInbounds(cfg); len(got) != 0 {
		t.Fatalf("TUN inbounds %q where the core cannot open one", got)
	}
}

// The generator read the countries only when a host named one, so a country used by
// cisco or OpenVPN alone found no SOCKS port and got no TUN.
func TestATunOnlyCountryIsReadFromTheDatabase(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t, model.PsiphonCountry{Code: "AT", Enabled: true, SocksPort: 11181, HTTPPort: 11381})
	saved := psiphonTunPossible
	psiphonTunPossible = func() bool { return true }
	t.Cleanup(func() { psiphonTunPossible = saved })

	cfg := &xray.Config{RouterConfig: json_util.RawMessage(`{"rules":[]}`)}
	injectExitRoutes(cfg, []*model.Inbound{{Id: 20, Tag: "cisco", Protocol: model.Cisco, Enable: true, Settings: ciscoSettings("psi-AT")}}, nil)
	if got := tunInbounds(cfg); len(got) != 1 || got[0] != psiphonTunAT {
		t.Fatalf("TUN inbounds %q; want %s", got, psiphonTunAT)
	}
}

// Turning off a country a cisco location or OpenVPN inbound leaves through would
// leave those customers held at a blackhole with nothing to say why.
func TestACountryACiscoOrOpenVPNInboundUsesStaysOn(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "AT", Enabled: true, SocksPort: 11181, HTTPPort: 11381},
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11182, HTTPPort: 11382},
	)
	for _, ib := range []*model.Inbound{
		{Tag: "c", Remark: "cisco-at", Port: 4443, Protocol: model.Cisco, Settings: ciscoSettings("wgr9", "psi-AT")},
		{Tag: "o", Remark: "ovpn-de", Port: 1194, Protocol: model.OpenVPN, Enable: true, Settings: `{"exit":"psi-DE"}`},
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &PsiphonService{}
	for keep, want := range map[string]string{
		"DE": "🇦🇹 Austria is still the exit of cisco-at",
		"AT": "🇩🇪 Germany is still the exit of ovpn-de",
	} {
		if err := svc.SetCountries([]string{keep}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("keeping only %s: got %v, want %q", keep, err, want)
		}
	}
}

func setupSidecarSaveTest(t *testing.T) *InboundService {
	t.Helper()
	setupConflictDB(t)
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	mgr.SetLocalRuntimeOverride(&fakeNodeRuntime{})
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "AT", Enabled: true, SocksPort: 11181, HTTPPort: 11381},
		model.PsiphonCountry{Code: "FR", Enabled: false, SocksPort: 11182, HTTPPort: 11382},
		model.PsiphonCountry{Code: "NL", Enabled: true, SocksPort: 11183, HTTPPort: 11383},
	)
	return &InboundService{}
}

// A cisco or OpenVPN save never touched Xray, so a new Psiphon country's TUN never
// came up; a save that moves no country must not reload it either.
func TestASaveReloadsXrayOnlyWhenItsPsiphonCountriesMove(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	seedInboundConflict(t, "cisco", "0.0.0.0", 4443, model.Cisco, "{}", ciscoSettings("wgr9"))
	for _, step := range []struct {
		name     string
		settings string
		reload   bool
	}{
		{"a relay country only", ciscoSettings("wgr9", ""), false},
		{"Psiphon AT added", ciscoSettings("wgr9", "psi-AT"), true},
		{"the same countries, reordered", ciscoSettings("psi-AT", "wgr9"), false},
		{"Psiphon NL added", ciscoSettings("psi-AT", "wgr9", "psi-NL"), true},
		{"the two Psiphon countries swapped", ciscoSettings("psi-NL", "wgr9", "psi-AT"), false},
		{"Psiphon AT and NL taken off", ciscoSettings("wgr9"), true},
	} {
		ib := loadInboundByTag(t, "cisco")
		ib.Settings = step.settings
		_, reload, err := svc.UpdateInbound(ib)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if reload != step.reload {
			t.Errorf("%s: reload=%v, want %v", step.name, reload, step.reload)
		}
	}
}

// A country that is not on would be saved and then held closed with no word why;
// the save says so instead, as a host's does. A relay exit is checked as before.
func TestASaveRefusesAPsiphonCountryThatIsNotOn(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	seedInboundConflict(t, "ovpn", "0.0.0.0", 1194, model.OpenVPN, "{}", `{"exit":"wg0"}`)
	ib := loadInboundByTag(t, "ovpn")
	ib.Settings = `{"exit":"psi-FR"}`
	if _, _, err := svc.UpdateInbound(ib); err == nil || !strings.Contains(err.Error(), "there is no enabled Psiphon country FR") {
		t.Fatalf("saving psi-FR: got %v, want it refused as not enabled", err)
	}
	ib.Settings = `{"exit":"wg9"}`
	if _, _, err := svc.UpdateInbound(ib); err != nil {
		t.Fatalf("an interface of the operator's own was refused: %v", err)
	}
}

// Adding, enabling, disabling or deleting a cisco inbound on a Psiphon country moves
// its TUN as much as an edit does; enabling without a reload leaves it held closed.
func TestEveryWayAPsiphonInboundComesOrGoesReloadsXray(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	added, reload, err := svc.AddInbound(&model.Inbound{
		Tag: "cisco", Port: 4443, Protocol: model.Cisco, Enable: true, StreamSettings: "{}", Settings: ciscoSettings("psi-AT"),
	})
	if err != nil || !reload {
		t.Fatalf("add: reload=%v err=%v, want a reload", reload, err)
	}
	for _, enable := range []bool{false, true} {
		if reload, err := svc.SetInboundEnable(added.Id, enable); err != nil || !reload {
			t.Errorf("enable=%v: reload=%v err=%v, want a reload", enable, reload, err)
		}
	}
	if reload, err := svc.DelInbound(added.Id); err != nil || !reload {
		t.Errorf("delete: reload=%v err=%v, want a reload", reload, err)
	}
}

// A country only a cisco location uses, with WARP on for some sites, routes those
// sites to its -warp outbound; a rule naming an outbound that is not there makes
// Xray refuse the whole config, every inbound with it.
func TestATunOnlyCountryGetsItsWarpOutbound(t *testing.T) {
	for _, mode := range []string{psiphonWarpSites, psiphonWarpAll} {
		cfg := &xray.Config{
			OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
			RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
		}
		psi := psiphonExits{
			socks: map[string]int{"AT": 11181}, tuns: true,
			warpMode: mode, warpSites: []string{"domain:gemini.google.com"}, warp: testWarpReg,
		}
		injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psi)

		if !strings.Contains(string(cfg.RouterConfig), `"outboundTag":"exit-psi-AT-warp"`) {
			t.Fatalf("%s: routing %s has no rule into WARP", mode, cfg.RouterConfig)
		}
		var outbounds []map[string]any
		if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(outbounds, func(o map[string]any) bool { return o["tag"] == "exit-psi-AT-warp" }) {
			t.Errorf("%s: no exit-psi-AT-warp outbound for the rule that names it", mode)
		}
	}
}

// Two inbounds of one tag stop Xray from starting any inbound. An operator's own
// inbound already tagged tun-psi-AT keeps the tag, and AT's pools stay held closed.
func TestATunTagAlreadyTakenIsNotAddedTwice(t *testing.T) {
	cfg := &xray.Config{
		InboundConfigs:  []xray.InboundConfig{{Tag: "tun-psi-AT", Protocol: "tun", Settings: json_util.RawMessage(`{"name":"mine"}`)}},
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181}, tuns: true})
	if got := tunInbounds(cfg); len(got) != 1 || !strings.Contains(got[0], `"name":"mine"`) {
		t.Errorf("TUN inbounds %q, want only the operator's own", got)
	}
	if strings.Contains(string(cfg.RouterConfig), "tun-psi-AT") {
		t.Errorf("routing %s steers the operator's inbound into Psiphon", cfg.RouterConfig)
	}
}

// After a start the routes go back on the TUNs it opened, found by the tag the
// generator gave them; a TUN the operator saved is not one of them.
func TestAStartFindsTheTunsItOpened(t *testing.T) {
	cfg := &xray.Config{
		InboundConfigs:  []xray.InboundConfig{{Tag: "inbound-tun", Protocol: "tun"}, {Tag: "tun-psi-NL", Protocol: "vless"}},
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181, "FR": 11182}, tuns: true})
	if got, want := psiphonTunDevices(cfg), []string{"psi-AT", "psi-FR"}; !slices.Equal(got, want) {
		t.Errorf("devices %q, want %q", got, want)
	}
}

// The pools' routes go back once the start's TUNs are up, or when the wait runs out:
// a device that never came keeps its pools held, and the jobs retry it.
func TestAStartPutsTheRoutesBackOnItsTuns(t *testing.T) {
	savedHere, savedUp, savedReassert, savedGap := psiphonTunsHere, psiphonTunsUp, reassertSidecars, psiphonTunPollGap
	t.Cleanup(func() {
		psiphonTunsHere, psiphonTunsUp, reassertSidecars, psiphonTunPollGap = savedHere, savedUp, savedReassert, savedGap
	})
	psiphonTunsHere, psiphonTunPollGap = true, time.Millisecond
	reasserted := make(chan struct{}, 4)
	reassertSidecars = func() { reasserted <- struct{}{} }
	var asked atomic.Int32
	psiphonTunsUp = func(devices []string) bool {
		if !slices.Equal(devices, []string{"psi-AT"}) {
			t.Errorf("waited on %q, want psi-AT", devices)
		}
		return asked.Add(1) >= 3
	}

	reassertSidecarExits(&xray.Config{InboundConfigs: []xray.InboundConfig{psiphonTunInbound("psi-AT")}})
	select {
	case <-reasserted:
	case <-time.After(5 * time.Second):
		t.Fatal("the routes were never put back after the start")
	}
	if n := asked.Load(); n < 3 {
		t.Errorf("put back after %d looks, want it to wait until the TUN is up", n)
	}

	psiphonTunsUp = func([]string) bool { return false }
	reassertWhenUp([]string{"psi-AT"}, 5*time.Millisecond)
	select {
	case <-reasserted:
	default:
		t.Error("a TUN that never came up left the routes unasserted")
	}

	reassertSidecarExits(&xray.Config{InboundConfigs: []xray.InboundConfig{{Tag: "inbound-tun", Protocol: "tun"}}})
	time.Sleep(50 * time.Millisecond)
	if len(reasserted) != 0 {
		t.Error("a start with no Psiphon TUN reasserted the sidecars' routes")
	}
}

// A new inbound is checked as an edit is: an OpenVPN inbound added on a country
// that is not on would be saved and then held closed with nothing to say why.
func TestAnAddRefusesAPsiphonCountryThatIsNotOn(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	_, _, err := svc.AddInbound(&model.Inbound{
		Tag: "ovpn", Port: 1194, Protocol: model.OpenVPN, Enable: true, StreamSettings: "{}", Settings: `{"exit":"psi-FR"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "there is no enabled Psiphon country FR") {
		t.Fatalf("adding psi-FR: got %v, want it refused as not enabled", err)
	}

	// A master's push was checked on the master; a node takes it as it is.
	sync := &InboundService{FromNodeSync: true}
	if _, _, err := sync.AddInbound(&model.Inbound{
		Tag: "ovpn-pushed", Port: 1195, Protocol: model.OpenVPN, Enable: true, StreamSettings: "{}", Settings: `{"exit":"psi-FR"}`,
	}); err != nil {
		t.Fatalf("a pushed row was refused: %v", err)
	}
}

// A cisco or OpenVPN pool on a node leaves from the node; this server's Psiphon is
// out of its reach, so a Psiphon exit there is refused by name.
func TestAPsiphonExitOnANodeIsRefused(t *testing.T) {
	setupSidecarSaveTest(t)
	node := 4
	ib := &model.Inbound{Remark: "remote-cisco", Protocol: model.Cisco, NodeID: &node, Settings: ciscoSettings("wgr9", "psi-AT")}
	if err := checkSidecarPsiphonExits(database.GetDB(), ib); err == nil || !strings.Contains(err.Error(), "remote-cisco runs on a node") {
		t.Errorf("a node's psi-AT: got %v, want it refused", err)
	}
	ib.Settings = ciscoSettings("wgr9")
	if err := checkSidecarPsiphonExits(database.GetDB(), ib); err != nil {
		t.Errorf("a node's relay exit is checked as before, not here: %v", err)
	}
}
