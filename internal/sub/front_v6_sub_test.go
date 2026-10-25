package sub

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const frontSubID = "front-sub"

// seedFrontCustomer puts one customer behind the front on every kind of inbound
// it carries, with a host inheriting the vless address and one on a CDN.
func seedFrontCustomer(t *testing.T) map[string]*model.Inbound {
	t.Helper()
	seedSubDB(t)
	// The page's country lookup refreshes in the background; two renders must agree.
	old := localCountryLabel
	localCountryLabel = func() string { return "" }
	t.Cleanup(func() { localCountryLabel = old })

	db := database.GetDB()
	serverPriv, _ := mustWireguardKeypair(t)
	clientPriv, _ := mustWireguardKeypair(t)
	rec := &model.ClientRecord{
		Email: "user", SubID: frontSubID, UUID: testFrontUUID, Password: "pw123456", Auth: "hyauth",
		PrivateKey: clientPriv, AllowedIPs: "10.0.0.2/32", Enable: true,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("client: %v", err)
	}
	client := fmt.Sprintf(`{"id":%q,"password":"pw123456","auth":"hyauth","email":"user","subId":%q,"enable":true}`, testFrontUUID, frontSubID)
	tlsStream := `{"network":"tcp","security":"tls","tlsSettings":{"alpn":["h2"]}}`
	inbounds := []*model.Inbound{
		{Tag: "vless", Protocol: model.VLESS, Port: 443, Settings: `{"clients":[` + client + `],"decryption":"none"}`, StreamSettings: tlsStream},
		{Tag: "vmess", Protocol: model.VMESS, Port: 2053, Settings: `{"clients":[` + client + `]}`, StreamSettings: tlsStream},
		{Tag: "trojan", Protocol: model.Trojan, Port: 2083, Settings: `{"clients":[` + client + `]}`, StreamSettings: tlsStream},
		{Tag: "hy", Protocol: model.Hysteria, Port: 8443, Settings: `{"version":2,"clients":[` + client + `]}`, StreamSettings: `{"network":"hysteria","security":"tls","tlsSettings":{"alpn":["h3"]}}`},
		{
			Tag: "wg", Protocol: model.WireGuard, Port: 51820, StreamSettings: `{}`,
			Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"user","privateKey":%q,"allowedIPs":["10.0.0.2/32"],"enable":true}]}`, serverPriv, clientPriv),
		},
		{Tag: "cisco", Protocol: model.Cisco, Port: 4443, Settings: `{}`, StreamSettings: `{}`},
		{Tag: "ovpn", Protocol: model.OpenVPN, Port: 1194, Settings: `{}`, StreamSettings: `{}`},
	}
	byTag := map[string]*model.Inbound{}
	for _, ib := range inbounds {
		ib.UserId, ib.Enable, ib.Remark, ib.Sniffing = 1, true, ib.Tag, "{}"
		ib.ShareAddrStrategy, ib.ShareAddr = "custom", testFrontName
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("inbound %s: %v", ib.Tag, err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
			t.Fatalf("attach %s: %v", ib.Tag, err)
		}
		byTag[ib.Tag] = ib
	}
	seedHost(t, &model.Host{InboundId: byTag["vless"].Id, SortOrder: 1, Remark: "A"})
	seedHost(t, &model.Host{InboundId: byTag["vless"].Id, SortOrder: 2, Remark: "CDN", Address: "cdn.example.com", Port: 8443})
	if err := db.Create(&xray.ClientTraffic{InboundId: byTag["vless"].Id, Email: "user", Enable: true}).Error; err != nil {
		t.Fatalf("traffic: %v", err)
	}
	return byTag
}

// seedFront adds the entry server the inbounds point at, handing out family.
func seedFront(t *testing.T, name, family string) {
	t.Helper()
	front := &model.Relay{
		Name: name, Address: testFrontIPv4, PublicName: testFrontName, KeyPath: "/etc/x-ui/relay-keys/" + name,
		Role: model.RelayFront, Enabled: true, Status: "ready", Address6: testFrontIPv6, IPFamily: family,
	}
	if err := database.GetDB().Create(front).Error; err != nil {
		t.Fatalf("front: %v", err)
	}
}

// frontSubOutput is everything one subscription renders, in every format.
type frontSubOutput struct {
	raw      []string
	json     string
	clash    string
	cisco    []CiscoLogin
	profiles []OpenVPNProfile
}

func renderFrontSub(t *testing.T) frontSubOutput {
	t.Helper()
	s := NewSubService("").ForRequest("sub.example.com")
	raw, _, _, _, err := s.getSubs(frontSubID)
	if err != nil {
		t.Fatalf("getSubs: %v", err)
	}
	js, _, err := NewSubJsonService("", "", "", "", NewSubService("")).GetJson(frontSubID, "sub.example.com", true)
	if err != nil {
		t.Fatalf("GetJson: %v", err)
	}
	clash, _, err := NewSubClashService(false, "", NewSubService("")).GetClash(frontSubID, "sub.example.com")
	if err != nil {
		t.Fatalf("GetClash: %v", err)
	}
	return frontSubOutput{raw: raw, json: js, clash: clash, cisco: s.ciscoLogins, profiles: s.openvpnProfiles}
}

// A front left on the default family gives every customer exactly what they had
// before the field existed, in every format; the same render does see "both".
func TestFrontEmptyIPFamilyChangesNothing(t *testing.T) {
	seedFrontCustomer(t)
	before := renderFrontSub(t)
	if len(before.raw) == 0 || before.json == "" || before.clash == "" || len(before.cisco) != 1 || len(before.profiles) != 1 {
		t.Fatalf("the seeded subscription renders nothing to compare: %+v", before)
	}

	seedFront(t, "front-ipv4", "")
	after := renderFrontSub(t)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("an IPv4 front changed the subscription\nbefore: %+v\n after: %+v", before, after)
	}

	seedFront(t, "front-both", "both")
	both := renderFrontSub(t)
	for name, out := range map[string]string{
		"raw": strings.Join(both.raw, "\n"), "json": both.json, "clash": both.clash,
	} {
		if !strings.Contains(out, testFrontIPv6) {
			t.Errorf("%s ignores a front handing out both families:\n%s", name, out)
		}
	}
}

// The original Clash core, which the legacy profile is for, dials no IPv6 unless
// the profile turns it on; an IPv4 front's profile never gains the key (above).
func TestFrontClashProfilesTurnIPv6On(t *testing.T) {
	seedFrontCustomer(t)
	seedFront(t, "front-v6", "v6")
	for _, legacy := range []bool{false, true} {
		name := map[bool]string{false: "mihomo", true: "legacy"}[legacy]
		out, _, err := NewSubClashService(false, "", NewSubService("")).getClash(frontSubID, "sub.example.com", legacy)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var doc struct {
			IPv6    bool `yaml:"ipv6"`
			Proxies []struct {
				Server string `yaml:"server"`
			} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(doc.Proxies) == 0 || doc.Proxies[0].Server != testFrontIPv6 || !doc.IPv6 {
			t.Errorf("%s profile dials %v with ipv6 %v; want %s with ipv6 on\n%s", name, doc.Proxies, doc.IPv6, testFrontIPv6, out)
		}
	}
}

// Only an enabled front that asks for IPv6 is loaded; a disabled one, or one
// left on IPv4, leaves its customers' addresses alone.
func TestLoadFronts6TakesOnlyEnabledFrontsAskingForIPv6(t *testing.T) {
	seedSubDB(t)
	db := database.GetDB()
	for _, r := range []*model.Relay{
		{Name: "on", Address: "198.51.100.1", PublicName: "on.example.com", IPFamily: "both"},
		{Name: "off", Address: "198.51.100.2", PublicName: "off.example.com", IPFamily: "both"},
		{Name: "ipv4", Address: "198.51.100.3", PublicName: "ipv4.example.com", IPFamily: ""},
	} {
		r.KeyPath, r.Role, r.Enabled, r.Address6 = "/k/"+r.Name, model.RelayFront, true, testFrontIPv6
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("relay %s: %v", r.Name, err)
		}
	}
	// A false bool is a zero value gorm replaces with the column default on create.
	if err := db.Model(&model.Relay{}).Where("name = ?", "off").Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}

	s := NewSubService("").ForRequest("sub.example.com")
	if s.frontNamed("on.example.com") == nil {
		t.Fatal("the enabled front handing out both families was not loaded")
	}
	for _, name := range []string{"off.example.com", "ipv4.example.com"} {
		if s.frontNamed(name) != nil {
			t.Errorf("%s was loaded as an IPv6 front", name)
		}
	}
}

// One customer behind a "both" front: every format gives each config's IPv6
// copy first and the IPv4 one after, cisco keeps the name, OpenVPN gets two remotes.
func TestFrontBothThroughEverySubscriptionFormat(t *testing.T) {
	inbounds := seedFrontCustomer(t)
	seedFront(t, "front-both", "both")
	out := renderFrontSub(t)
	v6 := "[" + testFrontIPv6 + "]"

	type entry struct{ remark, address string }
	want := []entry{
		{"vless-A-user · IPv6", v6},
		{"vless-A-user · IPv4", testFrontName},
		{"vless-CDN-user", "cdn.example.com"},
		{"vmess-user · IPv6", v6},
		{"vmess-user · IPv4", testFrontName},
		{"trojan-user · IPv6", v6},
		{"trojan-user · IPv4", testFrontName},
		{"hy-user · IPv6", v6},
		{"hy-user · IPv4", testFrontName},
		{"wg-user · IPv6", v6},
		{"wg-user · IPv4", testFrontName},
	}
	// The JSON and Clash outputs read the address bare where their format does.
	sameEntries := func(t *testing.T, format string, got []entry, bare bool) {
		t.Helper()
		expected := make([]entry, len(want))
		for i, w := range want {
			expected[i] = w
			if bare {
				expected[i].address = strings.Trim(w.address, "[]")
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s entries\n got: %v\nwant: %v", format, got, expected)
		}
	}

	t.Run("raw", func(t *testing.T) {
		var got []entry
		for _, link := range splitLinkLines(strings.Join(out.raw, "\n")) {
			if strings.HasPrefix(link, "vmess://") {
				host, _ := dialedVmess(t, link)
				ps, _ := vmessObject(t, link)["ps"].(string)
				got = append(got, entry{ps, hostOnly(host)})
				continue
			}
			u := mustParseLink(t, link)
			got = append(got, entry{u.Fragment, hostOnly(u.Host)})
		}
		sameEntries(t, "raw", got, false)
	})

	t.Run("json", func(t *testing.T) {
		var docs []struct {
			Remarks   string `json:"remarks"`
			Outbounds []struct {
				Protocol string         `json:"protocol"`
				Settings map[string]any `json:"settings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal([]byte(out.json), &docs); err != nil {
			t.Fatalf("json: %v", err)
		}
		var got []entry
		for _, d := range docs {
			if len(d.Outbounds) == 0 {
				t.Fatalf("json config %q has no outbounds", d.Remarks)
			}
			got = append(got, entry{d.Remarks, jsonOutboundAddress(d.Outbounds[0].Settings)})
		}
		sameEntries(t, "json", got, false)
	})

	t.Run("clash", func(t *testing.T) {
		var doc struct {
			Proxies []struct {
				Name   string `yaml:"name"`
				Server string `yaml:"server"`
			} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(out.clash), &doc); err != nil {
			t.Fatalf("clash: %v", err)
		}
		var got []entry
		for _, p := range doc.Proxies {
			got = append(got, entry{p.Name, p.Server})
		}
		sameEntries(t, "clash", got, true)
	})

	t.Run("cisco and openvpn", func(t *testing.T) {
		if len(out.cisco) != 1 || out.cisco[0].Server != testFrontName {
			t.Fatalf("cisco logins = %+v, want the front's name", out.cisco)
		}
		if len(out.profiles) != 1 || out.profiles[0].Address != testFrontName || out.profiles[0].Address6 != v6 {
			t.Fatalf("openvpn profiles = %+v, want %s tried before %s", out.profiles, v6, testFrontName)
		}
	})

	t.Run("panel links", func(t *testing.T) {
		var hy model.Inbound
		if err := database.GetDB().First(&hy, inbounds["hy"].Id).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		links := NewLinkProvider().LinksForClient("sub.example.com", &hy, "user")
		if len(links) != 2 || hostOnly(mustParseLink(t, links[0]).Host) != v6 || hostOnly(mustParseLink(t, links[1]).Host) != testFrontName {
			t.Fatalf("panel links = %q, want the IPv6 one then the IPv4 one", links)
		}
	})
}

// hostOnly drops the port from a host:port, keeping an IPv6 in its brackets.
func hostOnly(hostPort string) string {
	if i := strings.LastIndex(hostPort, ":"); i > strings.LastIndex(hostPort, "]") {
		return hostPort[:i]
	}
	return hostPort
}

// jsonOutboundAddress is where an xray outbound dials, in whichever shape its protocol writes it.
func jsonOutboundAddress(settings map[string]any) string {
	if addr, ok := settings["address"].(string); ok {
		return addr
	}
	if servers, _ := settings["servers"].([]any); len(servers) > 0 {
		server, _ := servers[0].(map[string]any)
		addr, _ := server["address"].(string)
		return addr
	}
	if peers, _ := settings["peers"].([]any); len(peers) > 0 {
		peer, _ := peers[0].(map[string]any)
		endpoint, _ := peer["endpoint"].(string)
		return hostOnly(endpoint)
	}
	return ""
}
