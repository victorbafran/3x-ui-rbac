package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func seedPsiphonCountries(t *testing.T, rows ...model.PsiphonCountry) {
	t.Helper()
	for i := range rows {
		if err := database.GetDB().Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].Code, err)
		}
	}
}

func seedPsiphonWorld(t *testing.T) map[string]int {
	t.Helper()
	ids := seedExitWorld(t)
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381},
		model.PsiphonCountry{Code: "FR", Enabled: false, SocksPort: 11182, HTTPPort: 11382},
		model.PsiphonCountry{Code: "US", Enabled: true, SocksPort: 11183, HTTPPort: 11383},
	)
	return ids
}

// A Psiphon country is an exit like a relay, while it is enabled: its route is
// its two letters as one number, and WireGuard still takes a single country.
func TestAPsiphonCountryIsAnExitWhileItIsEnabled(t *testing.T) {
	ids := seedPsiphonWorld(t)
	s := &HostService{}

	for _, tag := range []string{"vless", "hysteria"} {
		created, err := s.AddHostGroup(exitHost("DE "+tag, ids[tag], "psi-DE", ""))
		if err != nil {
			t.Fatalf("a %s host leaving through Psiphon DE was refused: %v", tag, err)
		}
		if created[0].Exit != "psi-DE" || created[0].VlessRoute != "17477" {
			t.Errorf("%s: stored exit %q route %q; want psi-DE and route 17477 ('D'<<8|'E')", tag, created[0].Exit, created[0].VlessRoute)
		}
	}
	wg, err := s.AddHostGroup(exitHost("DE wg", ids["wireguard"], "psi-DE", ""))
	if err != nil {
		t.Fatalf("a WireGuard host leaving through Psiphon DE was refused: %v", err)
	}
	if wg[0].VlessRoute != "" {
		t.Errorf("the WireGuard host got route %q; its link carries none", wg[0].VlessRoute)
	}

	for _, tc := range []struct {
		name, want string
		req        *entity.HostGroup
	}{
		{"a disabled country", "there is no enabled Psiphon country FR", exitHost("x", ids["vless"], "psi-FR", "")},
		{"a country never enabled", "there is no enabled Psiphon country NL", exitHost("x", ids["vless"], "psi-NL", "")},
		{"a code that is not two capitals", `"psi-de" is not a Psiphon country`, exitHost("x", ids["vless"], "psi-de", "")},
		{"vmess carries no route", "needs a VLESS, Hysteria or WireGuard inbound", exitHost("x", ids["vmess"], "psi-DE", "")},
		{"a node's traffic never reaches this server", "runs on a node", exitHost("x", ids["node-vless"], "psi-DE", "")},
		{"a second country on WireGuard", "wireguard already leaves through psi-DE", exitHost("x", ids["wireguard"], "psi-US", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.AddHostGroup(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

var testWarpReg = &psiphonWarpReg{
	PrivateKey: "-----BEGIN PRIVATE KEY-----\nk\n-----END PRIVATE KEY-----\n",
	PublicKey:  "peer=",
	Address:    []string{"172.16.0.2", "2606:4700:110:8a36::1"},
	Endpoint:   "162.159.198.1",
}

// Psiphon carries TCP only: a route's DNS leaves from here, its other UDP is
// refused so QUIC falls back to TCP, and the WARP sites go before the catch-all.
func TestAPsiphonRouteSendsDNSDirectRefusesUDPAndPutsWarpSitesFirst(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
		RouterConfig:    json_util.RawMessage(`{"rules":[{"type":"field","protocol":["bittorrent"],"outboundTag":"direct"}]}`),
	}
	inbounds := []*model.Inbound{
		{Id: 1, Tag: "in-443", Protocol: model.VLESS, Enable: true},
		{Id: 4, Tag: "wg-in", Protocol: model.WireGuard, Enable: true},
	}
	hosts := []*model.Host{
		{InboundId: 1, Exit: "psi-DE", VlessRoute: "17477"},
		{InboundId: 4, Exit: "psi-US"},
		{InboundId: 1, Exit: "psi-FR", VlessRoute: "18002"},
	}
	psi := psiphonExits{
		socks:     map[string]int{"DE": 11181, "US": 11183},
		warpMode:  psiphonWarpSites,
		warpSites: []string{"domain:gemini.google.com", "full:claude.ai"},
		warp:      testWarpReg,
	}
	injectExitRoutesWith(cfg, inbounds, hosts, psi)

	const sites = `"domain":["domain:gemini.google.com","full:claude.ai"],`
	const wantRouting = `{"rules":[{"outboundTag":"direct","protocol":["bittorrent"],"type":"field"},` +
		`{"inboundTag":["in-443"],"network":"udp","outboundTag":"direct","port":"53","type":"field","vlessRoute":"17477"},` +
		`{"inboundTag":["in-443"],"network":"udp","outboundTag":"blocked","type":"field","vlessRoute":"17477"},` +
		`{` + sites + `"inboundTag":["in-443"],"outboundTag":"exit-psi-DE-warp","type":"field","vlessRoute":"17477"},` +
		`{"inboundTag":["in-443"],"outboundTag":"exit-psi-DE","type":"field","vlessRoute":"17477"},` +
		`{"inboundTag":["wg-in"],"network":"udp","outboundTag":"direct","port":"53","type":"field"},` +
		`{"inboundTag":["wg-in"],"network":"udp","outboundTag":"blocked","type":"field"},` +
		`{` + sites + `"inboundTag":["wg-in"],"outboundTag":"exit-psi-US-warp","type":"field"},` +
		`{"inboundTag":["wg-in"],"outboundTag":"exit-psi-US","type":"field"},` +
		// FR is not enabled: blocked, never left to the default outbound and this server's IP.
		`{"inboundTag":["in-443"],"outboundTag":"blocked","type":"field","vlessRoute":"18002"}]}`
	if got := string(cfg.RouterConfig); got != wantRouting {
		t.Errorf("routing\n got %s\nwant %s", got, wantRouting)
	}

	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	var tags []string
	byTag := map[string]map[string]any{}
	for _, o := range outbounds {
		tag, _ := o["tag"].(string)
		tags = append(tags, tag)
		byTag[tag] = o
	}
	wantTags := []string{"direct", "exit-psi-DE", "exit-psi-DE-warp", "exit-psi-US", "exit-psi-US-warp", "blocked"}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Fatalf("outbounds %v; want %v: no country that is not enabled, and the missing blocked added once", tags, wantTags)
	}
	socks, _ := json.Marshal(byTag["exit-psi-DE"])
	if want := `{"protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":11181}]},"tag":"exit-psi-DE"}`; string(socks) != want {
		t.Errorf("Psiphon outbound\n got %s\nwant %s", socks, want)
	}
	warp, _ := json.Marshal(byTag["exit-psi-US-warp"])
	const wantWarp = `{"protocol":"masque","settings":{"address":"162.159.198.1","port":443},"streamSettings":{` +
		`"masqueSettings":{"warp":{"address":["172.16.0.2","2606:4700:110:8a36::1"],"privateKey":"-----BEGIN PRIVATE KEY-----\nk\n-----END PRIVATE KEY-----\n","publicKey":"peer="}},` +
		`"network":"masque","security":"tls","sockopt":{"dialerProxy":"exit-psi-US"},` +
		`"tlsSettings":{"alpn":["h2"],"serverName":"consumer-masque.cloudflareclient.com"}},"tag":"exit-psi-US-warp"}`
	if string(warp) != wantWarp {
		t.Errorf("WARP outbound\n got %s\nwant %s", warp, wantWarp)
	}
}

// WARP for all traffic takes the route's last rule; with no device registered
// there is no WARP at all, whatever the mode says.
func TestWarpForAllTakesTheLastRuleOnlyOnceRegistered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		warp     *psiphonWarpReg
		wantLast string
		wantTags []string
	}{
		{"registered", testWarpReg, "exit-psi-DE-warp", []string{"direct", "blocked", "exit-psi-DE", "exit-psi-DE-warp"}},
		{"not registered", nil, "exit-psi-DE", []string{"direct", "blocked", "exit-psi-DE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &xray.Config{
				OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`),
				RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
			}
			inbounds := []*model.Inbound{{Id: 1, Tag: "in-443", Protocol: model.VLESS, Enable: true}}
			hosts := []*model.Host{{InboundId: 1, Exit: "psi-DE", VlessRoute: "17477"}}
			psi := psiphonExits{socks: map[string]int{"DE": 11181}, warpMode: psiphonWarpAll, warpSites: []string{"domain:gemini.google.com"}, warp: tc.warp}
			injectExitRoutesWith(cfg, inbounds, hosts, psi)

			var routing struct {
				Rules []map[string]any `json:"rules"`
			}
			if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
				t.Fatal(err)
			}
			if len(routing.Rules) != 3 {
				t.Fatalf("rules %v; want DNS, UDP and the catch-all, and no sites rule", routing.Rules)
			}
			if got := routing.Rules[2]["outboundTag"]; got != tc.wantLast {
				t.Errorf("the catch-all leaves through %v; want %s", got, tc.wantLast)
			}
			var outbounds []map[string]any
			if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
				t.Fatal(err)
			}
			var tags []string
			for _, o := range outbounds {
				tags = append(tags, o["tag"].(string))
			}
			if !reflect.DeepEqual(tags, tc.wantTags) {
				t.Errorf("outbounds %v; want %v", tags, tc.wantTags)
			}
		})
	}
}

func psiphonHostsOn(t *testing.T, inboundId int) []model.Host {
	t.Helper()
	var hosts []model.Host
	if err := database.GetDB().Where("inbound_id = ?", inboundId).Order("sort_order asc, id asc").Find(&hosts).Error; err != nil {
		t.Fatal(err)
	}
	return hosts
}

// Each picked country is one more host on the inbound, copying its first host's
// connection; an inbound with no host keeps its own config through a plain one.
func TestAnInboundGetsOneHostPerPickedCountry(t *testing.T) {
	ids := seedPsiphonWorld(t)
	svc := &PsiphonService{}
	elsewhere := &entity.HostGroup{Remark: "elsewhere", InboundIds: []int{ids["vmess"]}, Hosts: []string{"x.example.ir"}, SortOrder: 4}
	if _, err := (&HostService{}).AddHostGroup(elsewhere); err != nil {
		t.Fatal(err)
	}

	if err := svc.SetInboundCountries(ids["vless"], []string{"de", "US"}); err != nil {
		t.Fatalf("picking DE and US: %v", err)
	}
	got := psiphonHostsOn(t, ids["vless"])
	type row struct {
		remark, exit, route string
		sort                int
	}
	var rows []row
	for _, h := range got {
		rows = append(rows, row{h.Remark, h.Exit, h.VlessRoute, h.SortOrder})
	}
	// Sorted after every host there is, so the inbound's own config stays first.
	want := []row{{"vless", "", "", 5}, {"🇩🇪 Germany", "psi-DE", "17477", 6}, {"🇺🇸 United States", "psi-US", "21843", 7}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("hosts %v; want %v", rows, want)
	}
	keptUS := got[2].Id

	if err := svc.SetInboundCountries(ids["vless"], []string{"US"}); err != nil {
		t.Fatalf("dropping DE: %v", err)
	}
	got = psiphonHostsOn(t, ids["vless"])
	if len(got) != 2 || got[0].Exit != "" || got[1].Exit != "psi-US" || got[1].Id != keptUS {
		t.Errorf("after dropping DE: %+v; want the plain host and the same US host", got)
	}

	// The disabled host comes first, and is passed over for the one in use.
	retired := &entity.HostGroup{Remark: "old", InboundIds: []int{ids["hysteria"]}, Hosts: []string{"old.example.ir"}, Sni: "old.example.ir", IsDisabled: true}
	base := &entity.HostGroup{
		Remark: "cdn", InboundIds: []int{ids["hysteria"]}, Hosts: []string{"cdn.example.ir:8443", "b.example.ir"},
		Sni: "front.example.ir", Fingerprint: "chrome", Alpn: []string{"h3"}, Security: "tls", Tags: []string{"cdn"},
	}
	for _, g := range []*entity.HostGroup{retired, base} {
		if _, err := (&HostService{}).AddHostGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SetInboundCountries(ids["hysteria"], []string{"DE"}); err != nil {
		t.Fatalf("picking DE on the hysteria inbound: %v", err)
	}
	var copies []model.Host
	for _, h := range psiphonHostsOn(t, ids["hysteria"]) {
		if h.Exit == "psi-DE" {
			copies = append(copies, h)
		}
	}
	if len(copies) != 2 {
		t.Fatalf("DE hosts %+v; want one per address of the copied host", copies)
	}
	for i, wantAddr := range []struct {
		addr string
		port int
	}{{"cdn.example.ir", 8443}, {"b.example.ir", 0}} {
		c := copies[i]
		if c.Address != wantAddr.addr || c.Port != wantAddr.port || c.Sni != "front.example.ir" || c.Fingerprint != "chrome" ||
			!reflect.DeepEqual(c.Alpn, []string{"h3"}) || c.Security != "tls" || c.Remark != "🇩🇪 Germany" || c.VlessRoute != "17477" {
			t.Errorf("DE host %d is %+v; want %s:%d with the copied TLS settings", i, c, wantAddr.addr, wantAddr.port)
		}
	}
	if n := len(psiphonHostsOn(t, ids["hysteria"])); n != 5 {
		t.Errorf("the hysteria inbound has %d hosts; want its three plus two DE copies, no plain host", n)
	}

	if err := svc.SetInboundCountries(ids["wireguard"], []string{"DE"}); err != nil {
		t.Fatalf("one country on WireGuard: %v", err)
	}
	if got := psiphonHostsOn(t, ids["wireguard"]); len(got) != 1 || got[0].Exit != "psi-DE" {
		t.Errorf("WireGuard hosts %+v; want the DE host alone, no plain one beside it", got)
	}

	for _, tc := range []struct {
		name    string
		inbound int
		codes   []string
		want    string
	}{
		{"two countries on WireGuard", ids["wireguard"], []string{"DE", "US"}, "it can leave through one country only"},
		{"a disabled country", ids["vless"], []string{"US", "FR"}, "there is no enabled Psiphon country FR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(psiphonHostsOn(t, tc.inbound))
			err := svc.SetInboundCountries(tc.inbound, tc.codes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
			if after := len(psiphonHostsOn(t, tc.inbound)); after != before {
				t.Errorf("the refused pick left %d hosts where there were %d", after, before)
			}
		})
	}
}

// A country still carrying a host's customers is not turned off, and a new one
// gets ports no other country holds and no inbound claims.
func TestTurningCountriesOnAndOff(t *testing.T) {
	ids := seedPsiphonWorld(t)
	for _, busy := range []*model.Inbound{
		{Tag: "on-11184", Port: 11184, Protocol: model.VLESS, Enable: true},
		{Tag: "on-11586", Listen: "10.0.0.1", Port: 11586, Protocol: model.VLESS, Enable: true},
	} {
		if err := database.GetDB().Create(busy).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&HostService{}).AddHostGroup(exitHost("DE", ids["hysteria"], "psi-DE", "")); err != nil {
		t.Fatal(err)
	}
	svc := &PsiphonService{}
	err := svc.SetCountries([]string{"US"})
	if want := "🇩🇪 Germany is still the exit of hysteria"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("turning off a country in use: got %v, want %q", err, want)
	}

	if err := svc.SetCountries([]string{"DE", "NL", "SE"}); err != nil {
		t.Fatalf("turning NL and SE on, US off: %v", err)
	}
	var rows []model.PsiphonCountry
	if err := database.GetDB().Order("code asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	type row struct {
		code         string
		enabled      bool
		socks, httpP int
	}
	var got []row
	for _, r := range rows {
		got = append(got, row{r.Code, r.Enabled, r.SocksPort, r.HTTPPort})
	}
	want := []row{
		{"DE", true, 11181, 11381},
		{"FR", false, 11182, 11382},
		{"NL", true, 11185, 11385},
		{"SE", true, 11187, 11387},
		{"US", false, 11183, 11383},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("countries %v\nwant %v: new ones skip the ports others hold, 11184 an inbound claims and 11186, whose redirect port 11586 one does", got, want)
	}
}

// A country's two loopback ports, and on every address its redirect port, stay its own:
// an inbound added later on one would stop Xray or the country binding. Disabled ones too.
func TestAnInboundCannotTakeAPsiphonCountrysPort(t *testing.T) {
	seedPsiphonWorld(t)
	svc := &InboundService{}
	for _, tc := range []struct {
		name string
		ib   *model.Inbound
		want string
	}{
		{"a SOCKS port", &model.Inbound{Tag: "a", Listen: "0.0.0.0", Port: 11181, Protocol: model.VLESS}, "psiphon-DE"},
		{"a disabled country's HTTP port", &model.Inbound{Tag: "b", Port: 11382, Protocol: model.VLESS}, "psiphon-FR"},
		{"another address than loopback", &model.Inbound{Tag: "c", Listen: "10.0.0.1", Port: 11181, Protocol: model.VLESS}, ""},
		{"UDP only", &model.Inbound{Tag: "d", Port: 11183, Protocol: model.Hysteria}, ""},
		{"a port no country holds", &model.Inbound{Tag: "e", Port: 11184, Protocol: model.VLESS}, ""},
		{"a redirect port, on any address", &model.Inbound{Tag: "f", Listen: "10.0.0.1", Port: 11581, Protocol: model.VLESS}, "redir-psi-DE"},
		{"a disabled country's redirect port", &model.Inbound{Tag: "g", Port: 11582, Protocol: model.VLESS}, "redir-psi-FR"},
		{"a redirect port, UDP only", &model.Inbound{Tag: "h", Port: 11583, Protocol: model.Hysteria}, ""},
		{"a redirect port no country holds", &model.Inbound{Tag: "i", Port: 11584, Protocol: model.VLESS}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.checkPortConflict(tc.ib, 0)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("refused by %+v; want it allowed", got)
			case tc.want != "" && (got == nil || got.Tag != tc.want):
				t.Errorf("got %+v; want a conflict with %s", got, tc.want)
			}
		})
	}
}
