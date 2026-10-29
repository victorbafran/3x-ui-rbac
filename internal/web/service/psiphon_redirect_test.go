package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

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

// redirectInbounds is the dokodemo-door inbounds of a config as Xray reads them.
func redirectInbounds(cfg *xray.Config) []string {
	var out []string
	for _, ib := range cfg.InboundConfigs {
		if ib.Protocol == "dokodemo-door" {
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

func emptyExitConfig() *xray.Config {
	return &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
	}
}

const psiphonRedirectAT = `{"listen":"0.0.0.0","port":11581,"protocol":"dokodemo-door",` +
	`"settings":{"network":"tcp","followRedirect":true},"tag":"redir-psi-AT",` +
	`"sniffing":{"enabled":true,"destOverride":["http","tls"],"routeOnly":false}}`

// Measured on the panel box: Xray's TUN fell to 0.55 MB/s at 2% loss where the kernel's
// REDIRECT into a dokodemo-door kept 4.8. One per country in use, while it and its inbound are on.
func TestACiscoOrOpenVPNPsiphonCountryGetsItsOwnRedirect(t *testing.T) {
	cfg := emptyExitConfig()
	// No host leaves through Psiphon at all: the redirect alone must still get its rules.
	psi := psiphonExits{socks: map[string]int{"AT": 11181, "US": 11183, "DE": 11184}}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psi)

	if got := redirectInbounds(cfg); len(got) != 1 || got[0] != psiphonRedirectAT {
		t.Fatalf("redirect inbounds %q\nwant only %s: none for a disabled country, a disabled inbound or a node", got, psiphonRedirectAT)
	}
	if got := psiphonRedirectPorts(cfg); !maps.Equal(got, map[string]int{"psi-AT": 11581}) {
		t.Errorf("the sidecars are sent to %v, want psi-AT on 11581", got)
	}
	const wantRouting = `{"rules":[` +
		`{"inboundTag":["redir-psi-AT"],"network":"udp","outboundTag":"direct","port":"53","type":"field"},` +
		`{"inboundTag":["redir-psi-AT"],"network":"udp","outboundTag":"blocked","type":"field"},` +
		`{"inboundTag":["redir-psi-AT"],"outboundTag":"exit-psi-AT","type":"field"}]}`
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

// Two inbounds taking TCP on one port stop Xray from starting any: an inbound saved on a
// redirect port before this existed keeps it, and that country's pools stay held.
func TestARedirectPortAnInboundTakesHoldsThePoolsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inbound  xray.InboundConfig
		redirect bool
	}{
		{"TCP", xray.InboundConfig{Tag: "mine", Port: 11581, Protocol: "vless"}, false},
		{"UDP only", xray.InboundConfig{Tag: "mine", Port: 11581, Protocol: "hysteria"}, true},
	} {
		cfg := emptyExitConfig()
		cfg.InboundConfigs = []xray.InboundConfig{tc.inbound}
		injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181}})
		if got := len(redirectInbounds(cfg)) == 1; got != tc.redirect {
			t.Errorf("%s on 11581: redirect added=%v, want %v", tc.name, got, tc.redirect)
		}
		if got := strings.Contains(string(cfg.RouterConfig), "redir-psi-AT"); got != tc.redirect {
			t.Errorf("%s on 11581: rules for the redirect=%v, want %v", tc.name, got, tc.redirect)
		}
	}
}

// The core never sees a cisco, OpenVPN or MTProto inbound, nor the panel: a redirect put on
// a port one took under the TUN build could not bind, and one such inbound stops every other.
func TestARedirectPortServedOutsideXrayHoldsThePoolsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		other    *model.Inbound
		panel    []int
		redirect bool
	}{
		{"a cisco inbound", &model.Inbound{Id: 30, Tag: "c2", Protocol: model.Cisco, Enable: true, Port: 11581, Settings: `{"disableUdp":true}`}, nil, false},
		{"an MTProto inbound", &model.Inbound{Id: 30, Tag: "mt", Protocol: model.MTProto, Enable: true, Port: 11581}, nil, false},
		{"OpenVPN over TCP", &model.Inbound{Id: 30, Tag: "ov", Protocol: model.OpenVPN, Enable: true, Port: 11581, Settings: `{"useTcp":true}`}, nil, false},
		{"OpenVPN over UDP", &model.Inbound{Id: 30, Tag: "ov", Protocol: model.OpenVPN, Enable: true, Port: 11581, Settings: `{}`}, nil, true},
		{"one switched off", &model.Inbound{Id: 30, Tag: "c2", Protocol: model.Cisco, Enable: false, Port: 11581}, nil, true},
		{"the panel", nil, []int{2053, 11581}, false},
	} {
		cfg := emptyExitConfig()
		inbounds := sidecarExitInbounds()
		if tc.other != nil {
			inbounds = append(inbounds, tc.other)
		}
		injectExitRoutesWith(cfg, inbounds, nil, psiphonExits{socks: map[string]int{"AT": 11181}, panelPorts: tc.panel})
		if got := len(redirectInbounds(cfg)) == 1; got != tc.redirect {
			t.Errorf("%s on 11581: redirect added=%v, want %v", tc.name, got, tc.redirect)
		}
	}
}

// A start sent the pools to ports the core had not opened yet, or never would; the ports
// are guarded first, recorded once the core runs them, and the pools sent round on a move.
func TestAStartGuardsThenRunsThenSendsThePools(t *testing.T) {
	savedGuard, savedRecord, savedReassert := guardRedirectPorts, recordRedirectPorts, reassertSidecars
	t.Cleanup(func() {
		guardRedirectPorts, recordRedirectPorts, reassertSidecars = savedGuard, savedRecord, savedReassert
	})
	var steps []string
	moved := false
	guardRedirectPorts = func(ports map[string]int) error { steps = append(steps, fmt.Sprint("guard ", ports)); return nil }
	recordRedirectPorts = func(ports map[string]int) (bool, error) {
		steps = append(steps, fmt.Sprint("record ", ports))
		return moved, nil
	}
	reassertSidecars = func() { steps = append(steps, "send the pools round") }

	cfg := emptyExitConfig()
	cfg.InboundConfigs = []xray.InboundConfig{{Tag: "vless", Port: 443, Protocol: "vless"}}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181}})
	const guard, record = "guard map[psi-AT:11581]", "record map[psi-AT:11581]"
	failed := errors.New("exit status 23")
	for _, tc := range []struct {
		name  string
		ran   bool
		err   error
		moved bool
		want  []string
	}{
		{"a start that opens a country", true, nil, true, []string{guard, "run", record, "send the pools round"}},
		{"a start on the same ports", true, nil, false, []string{guard, "run", record}},
		{"a start that failed", false, failed, true, []string{guard, "run"}},
		{"a hot apply the core refused", false, nil, true, []string{guard, "run"}},
	} {
		steps, moved = nil, tc.moved
		ran, err := runWithPsiphonRedirects(cfg, func() (bool, error) { steps = append(steps, "run"); return tc.ran, tc.err })
		if ran != tc.ran || !errors.Is(err, tc.err) || !slices.Equal(steps, tc.want) {
			t.Errorf("%s: ran=%v err=%v steps %q\nwant %q", tc.name, ran, err, steps, tc.want)
		}
	}
}

// The generator read the countries only when a host named one, so a country used by
// cisco or OpenVPN alone found no SOCKS port and got no redirect.
func TestAPoolOnlyCountryIsReadFromTheDatabase(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t, model.PsiphonCountry{Code: "AT", Enabled: true, SocksPort: 11181, HTTPPort: 11381})

	cfg := &xray.Config{RouterConfig: json_util.RawMessage(`{"rules":[]}`)}
	injectExitRoutes(cfg, []*model.Inbound{{Id: 20, Tag: "cisco", Protocol: model.Cisco, Enable: true, Settings: ciscoSettings("psi-AT")}}, nil)
	if got := redirectInbounds(cfg); len(got) != 1 || got[0] != psiphonRedirectAT {
		t.Fatalf("redirect inbounds %q; want %s", got, psiphonRedirectAT)
	}
}

// The panel and its subscription server listen outside Xray: a redirect on either's port
// would never bind, and the core with it. A subscription server switched off binds nothing.
func TestThePanelsOwnPortsAreReadFromTheSettings(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t, model.PsiphonCountry{Code: "AT", Enabled: true, SocksPort: 11181, HTTPPort: 11381})
	settings := &SettingService{}
	for _, tc := range []struct {
		name               string
		web, sub           int
		subEnabled, redirs bool
	}{
		{"the panel on 11581", 11581, 2096, true, false},
		{"the subscription server on 11581", 2053, 11581, true, false},
		{"a subscription server switched off", 2053, 11581, false, true},
	} {
		for key, value := range map[string]string{"webPort": fmt.Sprint(tc.web), "subPort": fmt.Sprint(tc.sub), "subEnable": fmt.Sprint(tc.subEnabled)} {
			if err := settings.saveSetting(key, value); err != nil {
				t.Fatal(err)
			}
		}
		cfg := &xray.Config{RouterConfig: json_util.RawMessage(`{"rules":[]}`)}
		injectExitRoutes(cfg, []*model.Inbound{{Id: 20, Tag: "cisco", Protocol: model.Cisco, Enable: true, Settings: ciscoSettings("psi-AT")}}, nil)
		if got := len(redirectInbounds(cfg)) == 1; got != tc.redirs {
			t.Errorf("%s: redirect added=%v, want %v", tc.name, got, tc.redirs)
		}
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

// A cisco or OpenVPN save never touched Xray, so a new Psiphon country's redirect never
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

// Adding, enabling, disabling or deleting a cisco inbound on a Psiphon country moves its
// redirect as much as an edit does; enabling without a reload leaves it held closed.
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

// A country only a cisco location uses, with WARP on for some sites, routes those sites
// to its -warp outbound; a rule naming an outbound that is not there fails the config.
func TestAPoolOnlyCountryGetsItsWarpOutbound(t *testing.T) {
	for _, mode := range []string{psiphonWarpSites, psiphonWarpAll} {
		cfg := emptyExitConfig()
		psi := psiphonExits{
			socks:    map[string]int{"AT": 11181},
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

// An operator's own inbound already tagged redir-psi-AT keeps the tag, AT's pools stay
// held, and the sidecars are never sent into it: it routes by the operator's rules.
func TestARedirectTagAlreadyTakenIsNotAddedTwice(t *testing.T) {
	cfg := emptyExitConfig()
	cfg.InboundConfigs = []xray.InboundConfig{{Tag: "redir-psi-AT", Port: 2000, Protocol: "dokodemo-door", Settings: json_util.RawMessage(`{"network":"tcp","followRedirect":true}`)}}
	injectExitRoutesWith(cfg, sidecarExitInbounds(), nil, psiphonExits{socks: map[string]int{"AT": 11181}})
	if got := redirectInbounds(cfg); len(got) != 1 || !strings.Contains(got[0], `"port":2000`) {
		t.Errorf("redirect inbounds %q, want only the operator's own", got)
	}
	if strings.Contains(string(cfg.RouterConfig), "redir-psi-AT") {
		t.Errorf("routing %s steers the operator's inbound into Psiphon", cfg.RouterConfig)
	}
	if got := psiphonRedirectPorts(cfg); len(got) != 0 {
		t.Errorf("the sidecars are sent to %v, the operator's inbound", got)
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

// An inbound saved on a redirect port before redirect ports existed could then no longer be
// edited, nor switched back on; it keeps the port, and a pool's exit there is refused by name.
func TestAnInboundOnARedirectPortFromBeforeKeepsIt(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	seedInboundConflict(t, "old", "0.0.0.0", 11581, model.VLESS, "{}", `{"clients":[],"decryption":"none"}`)
	ib := loadInboundByTag(t, "old")
	ib.Remark = "older"
	if _, _, err := svc.UpdateInbound(ib); err != nil {
		t.Fatalf("an edit that keeps the port: %v", err)
	}
	for _, enable := range []bool{false, true} {
		if _, err := svc.SetInboundEnable(ib.Id, enable); err != nil {
			t.Errorf("enable=%v: %v", enable, err)
		}
	}
	ib = loadInboundByTag(t, "old")
	ib.Port = 11583
	if _, _, err := svc.UpdateInbound(ib); err == nil || !strings.Contains(err.Error(), "redir-psi-NL") {
		t.Errorf("a move onto NL's redirect port: %v, want it refused", err)
	}

	cisco := func(exit string) *model.Inbound {
		return &model.Inbound{Tag: "cisco-" + exit, Port: 4443, Protocol: model.Cisco, Enable: true, StreamSettings: "{}", Settings: ciscoSettings(exit)}
	}
	if _, _, err := svc.AddInbound(cisco("psi-AT")); err == nil || !strings.Contains(err.Error(), "TCP port 11581, which older already uses") {
		t.Errorf("a cisco location on AT: %v, want the inbound on 11581 named", err)
	}
	if _, _, err := svc.AddInbound(cisco("psi-NL")); err != nil {
		t.Errorf("a cisco location on NL, whose port is free: %v", err)
	}
}

// redir-psi- tags are the Psiphon redirects': an inbound given one, a reseller's too, held
// the owner's cisco and OpenVPN pools on that country closed with nothing on screen to say why.
func TestNoInboundIsGivenAPsiphonRedirectsTag(t *testing.T) {
	svc := setupSidecarSaveTest(t)
	vless := `{"clients":[],"decryption":"none"}`
	added, _, err := svc.AddInbound(&model.Inbound{Tag: "redir-psi-AT", Port: 2001, Protocol: model.VLESS, Enable: true, StreamSettings: "{}", Settings: vless})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	seedInboundConflict(t, "mine", "0.0.0.0", 2002, model.VLESS, "{}", vless)
	renamed := loadInboundByTag(t, "mine")
	renamed.Tag = "redir-psi-NL"
	if _, _, err := svc.UpdateInbound(renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	for _, id := range []int{added.Id, renamed.Id} {
		var row model.Inbound
		if err := database.GetDB().First(&row, id).Error; err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(row.Tag, "redir-psi-") {
			t.Errorf("inbound %d was saved as %s", id, row.Tag)
		}
	}

	// A row stored with one before keeps it, and a pool sent to that country is refused by name.
	seedInboundConflict(t, "redir-psi-AT", "0.0.0.0", 2003, model.VLESS, "{}", vless)
	legacy := loadInboundByTag(t, "redir-psi-AT")
	legacy.Remark = "older"
	if _, _, err := svc.UpdateInbound(legacy); err != nil {
		t.Fatalf("an edit of the older row: %v", err)
	}
	loadInboundByTag(t, "redir-psi-AT")
	cisco := func(exit string) *model.Inbound {
		return &model.Inbound{Tag: "cisco-" + exit, Port: 4443, Protocol: model.Cisco, Enable: true, StreamSettings: "{}", Settings: ciscoSettings(exit)}
	}
	if _, _, err := svc.AddInbound(cisco("psi-AT")); err == nil || !strings.Contains(err.Error(), "tagged redir-psi-AT, which older already holds") {
		t.Errorf("a cisco location on AT: %v, want the inbound holding its tag named", err)
	}
	if _, _, err := svc.AddInbound(cisco("psi-NL")); err != nil {
		t.Errorf("a cisco location on NL, whose tag is free: %v", err)
	}
}
