package sub

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

const (
	testFrontName = "tun.example.com"
	testFrontIPv4 = "185.0.0.1"
	testFrontIPv6 = "2001:db8::1"
	testFrontUUID = "11111111-2222-4333-8444-555555555555"
	// testV4Suffix is remarkSuffixV4 as a link fragment escapes it.
	testV4Suffix = "%20%C2%B7%20IPv4"
)

// frontRequest is a request that knows one front, handing out family.
func frontRequest(t *testing.T, family string) *SubService {
	t.Helper()
	f, ok := frontFromRelay(model.Relay{
		Role: model.RelayFront, Enabled: true, Address: testFrontIPv4,
		PublicName: testFrontName, Address6: testFrontIPv6, IPFamily: family,
	})
	if !ok {
		t.Fatalf("a front set to %q hands out no IPv6", family)
	}
	return &SubService{fronts6: []front6{f}}
}

// behindFront is an inbound whose customers are sent to address, the way the
// inbound form's front picker sets the share address.
func behindFront(protocol model.Protocol, port int, settings, stream, address string) *model.Inbound {
	return &model.Inbound{
		Id: 7, Port: port, Protocol: protocol, Remark: "r", Settings: settings,
		StreamSettings: stream, ShareAddrStrategy: "custom", ShareAddr: address,
	}
}

const frontVlessSettings = `{"clients":[{"id":"` + testFrontUUID + `","email":"user"}],"decryption":"none","encryption":"none"}`

// rawFrontLinks is one client's raw links the way getSubs renders them.
func rawFrontLinks(t *testing.T, s *SubService, inbound *model.Inbound, hostEps []map[string]any) []string {
	t.Helper()
	client, ok := s.clientForLink(inbound, "user")
	if !ok {
		t.Fatal("the inbound has no client user")
	}
	return splitLinkLines(s.clientLink(inbound, client, hostEps, s.frontUnits(inbound, rawEndpoints(inbound, hostEps))))
}

func mustParseLink(t *testing.T, link string) *url.URL {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse %q: %v", link, err)
	}
	return u
}

// Only "v6" and "both" hand out IPv6, and only with an IPv6 to hand out; an
// empty family is the IPv4 every front had before, so it must read as no front.
func TestFrontFromRelay(t *testing.T) {
	for _, tc := range []struct {
		name, family, address6 string
		want                   bool
		both                   bool
	}{
		{name: "empty family is IPv4", family: "", address6: testFrontIPv6},
		{name: "unknown family", family: "v4", address6: testFrontIPv6},
		{name: "v6", family: "v6", address6: testFrontIPv6, want: true},
		{name: "both", family: "both", address6: testFrontIPv6, want: true, both: true},
		{name: "no address yet", family: "both", address6: ""},
		{name: "an IPv4 is not an IPv6", family: "v6", address6: "203.0.113.9"},
		{name: "mapped IPv4 is not an IPv6", family: "v6", address6: "::ffff:203.0.113.9"},
		{name: "bracketed upper case", family: "v6", address6: "[2001:DB8::1]", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := frontFromRelay(model.Relay{Address: testFrontIPv4, Address6: tc.address6, IPFamily: tc.family})
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			if f.address6 != "["+testFrontIPv6+"]" || f.both != tc.both {
				t.Fatalf("front = %+v, want address6 [%s] both=%v", f, testFrontIPv6, tc.both)
			}
		})
	}
}

// An address names the front however it is spelled; a different host never does.
func TestFrontNamedMatchesEverySpelling(t *testing.T) {
	s := frontRequest(t, "both")
	for _, addr := range []string{testFrontName, "TUN.Example.COM", testFrontName + ".", " " + testFrontName, testFrontIPv4, "[" + testFrontIPv4 + "]"} {
		if s.frontNamed(addr) == nil {
			t.Errorf("%q does not name the front", addr)
		}
	}
	for _, addr := range []string{"", "tun.example.com.evil.net", "panel.example.com", testFrontIPv6, "185.0.0.10"} {
		if s.frontNamed(addr) != nil {
			t.Errorf("%q names the front", addr)
		}
	}
}

func TestFrontV6OnlyGivesTheIPv6Link(t *testing.T) {
	s := frontRequest(t, "v6")
	inbound := behindFront(model.VLESS, 443, frontVlessSettings, `{"network":"tcp","security":"tls","tlsSettings":{"alpn":["h2"]}}`, testFrontName)

	links := rawFrontLinks(t, s, inbound, nil)
	if len(links) != 1 {
		t.Fatalf("links = %q, want only the IPv6 one", links)
	}
	u := mustParseLink(t, links[0])
	if u.Host != "["+testFrontIPv6+"]:443" || u.Fragment != "r-user" {
		t.Fatalf("link = %s, want [%s]:443 named r-user", links[0], testFrontIPv6)
	}
	// The IPv4 link's client sent the front's name as SNI; the literal sends none.
	if sni := u.Query().Get("sni"); sni != testFrontName {
		t.Fatalf("sni = %q, want the name the address gave, %q", sni, testFrontName)
	}
}

// "both" is the IPv6 link first and then exactly the link the customer had
// before, told apart only by the remark.
func TestFrontBothGivesIPv6ThenTheUntouchedIPv4(t *testing.T) {
	stream := `{"network":"ws","security":"tls","wsSettings":{"path":"/p"},"tlsSettings":{"alpn":["h2"]}}`
	plain := rawFrontLinks(t, &SubService{}, behindFront(model.VLESS, 443, frontVlessSettings, stream, testFrontName), nil)
	if len(plain) != 1 {
		t.Fatalf("plain links = %q, want one", plain)
	}

	s := frontRequest(t, "both")
	links := rawFrontLinks(t, s, behindFront(model.VLESS, 443, frontVlessSettings, stream, testFrontName), nil)
	if len(links) != 2 {
		t.Fatalf("links = %q, want IPv6 then IPv4", links)
	}
	v6 := mustParseLink(t, links[0])
	if v6.Host != "["+testFrontIPv6+"]:443" || v6.Fragment != "r-user · IPv6" || v6.Query().Get("sni") != testFrontName {
		t.Fatalf("first link = %s, want the IPv6 one carrying sni %s", links[0], testFrontName)
	}
	if links[1] != plain[0]+testV4Suffix {
		t.Fatalf("second link\n got: %s\nwant: %s", links[1], plain[0]+testV4Suffix)
	}

	// The next inbound of the same subscription is not behind the front.
	other := behindFront(model.VLESS, 8443, frontVlessSettings, stream, "other.example.com")
	other.Id = 8
	next := rawFrontLinks(t, s, other, nil)
	if len(next) != 1 || mustParseLink(t, next[0]).Fragment != "r-user" || mustParseLink(t, next[0]).Host != "other.example.com:8443" {
		t.Fatalf("the front's rendering leaked into the next inbound: %q", next)
	}
}

// Two hosts reaching the front by different spellings each keep their own name:
// the one dialling its IPv4 sent none, the one dialling its name sent that.
func TestFrontHostsKeepTheNameOfTheirOwnSpelling(t *testing.T) {
	s := frontRequest(t, "v6")
	inbound := behindFront(model.VLESS, 443, frontVlessSettings, `{"network":"tcp","security":"tls"}`, testFrontName)
	defaultDest := s.resolveInboundAddress(inbound)
	hostEps := []map[string]any{
		hostToExternalProxyMap(&model.Host{Id: 1, Remark: "IP", Address: testFrontIPv4}, defaultDest, inbound.Port),
		hostToExternalProxyMap(&model.Host{Id: 2, Remark: "N"}, defaultDest, inbound.Port),
	}
	links := rawFrontLinks(t, s, inbound, hostEps)
	want := []struct{ remark, sni string }{{"r-IP-user", ""}, {"r-N-user", testFrontName}}
	if len(links) != len(want) {
		t.Fatalf("links = %q, want %d", links, len(want))
	}
	for i, w := range want {
		u := mustParseLink(t, links[i])
		if u.Host != "["+testFrontIPv6+"]:443" || u.Fragment != w.remark || u.Query().Get("sni") != w.sni {
			t.Fatalf("link %d = %s, want the IPv6 named %q with sni %q", i, links[i], w.remark, w.sni)
		}
	}
}

// The name written into the IPv6 link is the one the IPv4 link already sent:
// an explicit one stays, a reality name is never replaced, and an IP gave none.
func TestFrontV6KeepsTheTLSNameTheIPv4LinkHad(t *testing.T) {
	reality := `{"network":"tcp","security":"reality","realitySettings":{"serverNames":["www.samsung.com"],"shortIds":["ab"],"settings":{"publicKey":"pbk","fingerprint":"chrome"}}}`
	for _, tc := range []struct {
		name, stream, address, wantSNI string
	}{
		{"name from the address", `{"network":"tcp","security":"tls"}`, testFrontName, testFrontName},
		{"explicit name stays", `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"panel.example.com"}}`, testFrontName, "panel.example.com"},
		{"front reached by its IPv4", `{"network":"tcp","security":"tls"}`, testFrontIPv4, ""},
		{"reality keeps its cover", reality, testFrontName, "www.samsung.com"},
		{"plain stream", `{"network":"tcp","security":"none"}`, testFrontName, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links := rawFrontLinks(t, frontRequest(t, "v6"), behindFront(model.VLESS, 443, frontVlessSettings, tc.stream, tc.address), nil)
			if len(links) != 1 {
				t.Fatalf("links = %q, want one", links)
			}
			u := mustParseLink(t, links[0])
			if u.Host != "["+testFrontIPv6+"]:443" {
				t.Fatalf("host = %s, want the IPv6", u.Host)
			}
			if sni := u.Query().Get("sni"); sni != tc.wantSNI {
				t.Fatalf("sni = %q, want %q", sni, tc.wantSNI)
			}
		})
	}
}

// Only a TLS stream takes its name from the address. A reality or plain one is
// copied byte for byte, so its JSON config grows no stray tlsSettings.
func TestWithTLSNameLeavesOtherStreamsAlone(t *testing.T) {
	for _, stream := range []string{
		`{"network":"tcp","security":"reality","realitySettings":{"serverNames":["www.samsung.com"]}}`,
		`{"network":"ws","security":"none","wsSettings":{"path":"/p"}}`,
	} {
		inbound := behindFront(model.VLESS, 443, frontVlessSettings, stream, testFrontName)
		if got := withTLSName(inbound, testFrontName).StreamSettings; got != stream {
			t.Errorf("stream rewritten\n got: %s\nwant: %s", got, stream)
		}
	}
}

// dialedFn reads where a link connects and which TLS name it sends.
type dialedFn func(t *testing.T, link string) (hostPort, sni string)

func dialedURI(t *testing.T, link string) (string, string) {
	u := mustParseLink(t, link)
	return u.Host, u.Query().Get("sni")
}

func vmessObject(t *testing.T, link string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatalf("decode vmess %q: %v", link, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("vmess json: %v", err)
	}
	return obj
}

func dialedVmess(t *testing.T, link string) (string, string) {
	obj := vmessObject(t, link)
	add, _ := obj["add"].(string)
	port, _ := obj["port"].(float64)
	sni, _ := obj["sni"].(string)
	return net.JoinHostPort(strings.Trim(add, "[]"), strconv.Itoa(int(port))), sni
}

// Telegram resolves a bracketed server as a host name, so the literal must be bare.
func dialedMtproto(t *testing.T, link string) (string, string) {
	q := mustParseLink(t, link).Query()
	return net.JoinHostPort(q.Get("server"), q.Get("port")), ""
}

func dialedAmneziaWG(t *testing.T, link string) (string, string) {
	for line := range strings.SplitSeq(decodeAmneziaWGSubLink(t, link), "\n") {
		if endpoint, ok := strings.CutPrefix(line, "Endpoint = "); ok {
			return endpoint, ""
		}
	}
	t.Fatalf("no Endpoint line in %s", link)
	return "", ""
}

// Every protocol a front carries hands out its IPv6 first, in the spelling its
// format takes, and keeps the name the TLS ones need.
func TestFrontBothCoversEveryProtocol(t *testing.T) {
	serverPriv, serverPub := mustWireguardKeypair(t)
	clientPriv, _ := mustWireguardKeypair(t)
	wgClient := `"email":"user","privateKey":"` + clientPriv + `","allowedIPs":["10.0.0.2/32"]`
	tlsStream := `{"network":"tcp","security":"tls","tlsSettings":{}}`
	for _, tc := range []struct {
		name     string
		protocol model.Protocol
		port     int
		settings string
		stream   string
		dialed   dialedFn
		sni      string
	}{
		{"vless", model.VLESS, 443, frontVlessSettings, tlsStream, dialedURI, testFrontName},
		{"vmess", model.VMESS, 443, `{"clients":[{"id":"` + testFrontUUID + `","email":"user","security":"auto"}]}`, tlsStream, dialedVmess, testFrontName},
		{"trojan", model.Trojan, 443, `{"clients":[{"password":"pw","email":"user"}]}`, tlsStream, dialedURI, testFrontName},
		{"shadowsocks", model.Shadowsocks, 8388, `{"method":"aes-256-gcm","clients":[{"password":"pw","email":"user"}]}`, `{"network":"tcp","security":"none"}`, dialedURI, ""},
		// A hysteria stream need not say "tls": the protocol always is.
		{"hysteria", model.Hysteria, 443, `{"version":2,"clients":[{"auth":"hyauth","email":"user"}]}`, `{"network":"hysteria","tlsSettings":{"alpn":["h3"]}}`, dialedURI, testFrontName},
		{"tuic", model.TUIC, 443, `{"server":{"certificate":"/c","private_key":"/k"},"clients":[{"uuid":"` + testFrontUUID + `","password":"pw","email":"user"}]}`, `{}`, dialedURI, testFrontName},
		{"wireguard", model.WireGuard, 51820, `{"secretKey":"` + serverPriv + `","clients":[{` + wgClient + `}]}`, `{}`, dialedURI, ""},
		{"amneziawg", model.AmneziaWG, 51821, `{"server":{"privateKey":"` + serverPriv + `","publicKey":"` + serverPub + `"},"clients":[{` + wgClient + `}]}`, `{}`, dialedAmneziaWG, ""},
		{"mtproto", model.MTProto, 8443, `{"clients":[{"email":"user","secret":"` + mtprotoTestSecret + `"}]}`, `{}`, dialedMtproto, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links := rawFrontLinks(t, frontRequest(t, "both"), behindFront(tc.protocol, tc.port, tc.settings, tc.stream, testFrontName), nil)
			if len(links) != 2 {
				t.Fatalf("links = %q, want IPv6 then IPv4", links)
			}
			port := strconv.Itoa(tc.port)
			if host, sni := tc.dialed(t, links[0]); host != "["+testFrontIPv6+"]:"+port || sni != tc.sni {
				t.Fatalf("first link dials %s with sni %q, want [%s]:%s with %q\n%s", host, sni, testFrontIPv6, port, tc.sni, links[0])
			}
			if host, _ := tc.dialed(t, links[1]); host != testFrontName+":"+port {
				t.Fatalf("second link dials %s, want %s:%s", host, testFrontName, port)
			}
		})
	}
}

// A host that inherits the inbound's address is the front's, so it gets its
// IPv6 pair; a host with an address of its own (a CDN) is left as it was.
func TestFrontHostsThatInheritGetTheIPv6(t *testing.T) {
	stream := `{"network":"tcp","security":"tls"}`
	for _, tc := range []struct {
		family string
		want   []struct{ host, remark, sni string }
	}{
		{"both", []struct{ host, remark, sni string }{
			{"[" + testFrontIPv6 + "]:443", "r-A-user · IPv6", testFrontName},
			{testFrontName + ":443", "r-A-user · IPv4", ""},
			{"cdn.example.com:8443", "r-CDN-user", ""},
		}},
		{"v6", []struct{ host, remark, sni string }{
			{"[" + testFrontIPv6 + "]:443", "r-A-user", testFrontName},
			{"cdn.example.com:8443", "r-CDN-user", ""},
		}},
	} {
		t.Run(tc.family, func(t *testing.T) {
			s := frontRequest(t, tc.family)
			inbound := behindFront(model.VLESS, 443, frontVlessSettings, stream, testFrontName)
			defaultDest := s.resolveInboundAddress(inbound)
			hostEps := []map[string]any{
				hostToExternalProxyMap(&model.Host{Id: 1, Remark: "A"}, defaultDest, inbound.Port),
				hostToExternalProxyMap(&model.Host{Id: 2, Remark: "CDN", Address: "cdn.example.com", Port: 8443}, defaultDest, inbound.Port),
			}
			links := rawFrontLinks(t, s, inbound, hostEps)
			if len(links) != len(tc.want) {
				t.Fatalf("links = %q, want %d", links, len(tc.want))
			}
			for i, w := range tc.want {
				u := mustParseLink(t, links[i])
				if u.Host != w.host || u.Fragment != w.remark || u.Query().Get("sni") != w.sni {
					t.Fatalf("link %d = %s, want %s named %q with sni %q", i, links[i], w.host, w.remark, w.sni)
				}
			}
		})
	}
}

// A front's IPv6 takes its host's place in the operator's list: a CDN host sorted
// first stays first, and "both" puts the IPv4 copy right after its IPv6 one.
func TestFrontHostsKeepTheOperatorsOrder(t *testing.T) {
	stream := `{"network":"tcp","security":"tls"}`
	type entry struct{ host, remark string }
	cdn := entry{"cdn.example.com:8443", "r-CDN-user"}
	for _, tc := range []struct {
		family string
		want   []entry
	}{
		{"v6", []entry{cdn, {"[" + testFrontIPv6 + "]:443", "r-A-user"}}},
		{"both", []entry{cdn, {"[" + testFrontIPv6 + "]:443", "r-A-user · IPv6"}, {testFrontName + ":443", "r-A-user · IPv4"}}},
	} {
		t.Run(tc.family, func(t *testing.T) {
			s := frontRequest(t, tc.family)
			inbound := behindFront(model.VLESS, 443, frontVlessSettings, stream, testFrontName)
			defaultDest := s.resolveInboundAddress(inbound)
			hostEps := []map[string]any{
				hostToExternalProxyMap(&model.Host{Id: 2, Remark: "CDN", Address: "cdn.example.com", Port: 8443}, defaultDest, inbound.Port),
				hostToExternalProxyMap(&model.Host{Id: 1, Remark: "A"}, defaultDest, inbound.Port),
			}
			var got []entry
			for _, link := range rawFrontLinks(t, s, inbound, hostEps) {
				u := mustParseLink(t, link)
				got = append(got, entry{u.Host, u.Fragment})
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("links\n got: %v\nwant: %v", got, tc.want)
			}
		})
	}
}

// mihomo refuses an IPv6 literal on a proxy pinned to "ipv4", so a host's pin
// stays on its IPv4 copy and the IPv6 copy dials the literal as it is.
func TestFrontClashIPv6CopyDropsAnIPv4Pin(t *testing.T) {
	s := frontRequest(t, "both")
	inbound := behindFront(model.VLESS, 443, frontVlessSettings, `{"network":"tcp","security":"tls"}`, testFrontName)
	injectExternalProxy(inbound, []map[string]any{
		hostToExternalProxyMap(&model.Host{Id: 1, Remark: "A", MihomoIpVersion: "ipv4"}, s.resolveInboundAddress(inbound), inbound.Port),
	})
	clash := &SubClashService{SubService: s}
	client := model.Client{ID: testFrontUUID, Email: "user"}
	proxies := clash.clientProxies(s, inbound, client, "sub.example.com", s.frontUnits(inbound, streamEndpoints(inbound)))
	if len(proxies) != 2 {
		t.Fatalf("proxies = %v, want the IPv6 one then the IPv4 one", proxies)
	}
	if proxies[0]["server"] != testFrontIPv6 || proxies[0]["ip-version"] != nil {
		t.Errorf("IPv6 proxy dials %v with ip-version %v; want %s with none", proxies[0]["server"], proxies[0]["ip-version"], testFrontIPv6)
	}
	if proxies[1]["server"] != testFrontName || proxies[1]["ip-version"] != "ipv4" {
		t.Errorf("IPv4 proxy dials %v with ip-version %v; want %s pinned to ipv4", proxies[1]["server"], proxies[1]["ip-version"], testFrontName)
	}
}

// An externalProxy entry naming the front pairs like the inbound's own address
// does, and one forcing TLS onto a plain inbound keeps the name it was sent with.
func TestFrontExternalProxyNamingTheFrontIsPaired(t *testing.T) {
	stream := `{"network":"tcp","security":"none","externalProxy":[` +
		`{"forceTls":"tls","dest":"` + testFrontName + `","port":443,"remark":"F"},` +
		`{"forceTls":"same","dest":"cdn.example.com","port":8443,"remark":"C"}]}`
	links := rawFrontLinks(t, frontRequest(t, "both"), behindFront(model.VLESS, 80, frontVlessSettings, stream, "panel.example.com"), nil)
	want := []struct{ host, remark, security, sni string }{
		{"[" + testFrontIPv6 + "]:443", "r-F-user · IPv6", "tls", testFrontName},
		{testFrontName + ":443", "r-F-user · IPv4", "tls", ""},
		{"cdn.example.com:8443", "r-C-user", "none", ""},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %q, want %d", links, len(want))
	}
	for i, w := range want {
		u := mustParseLink(t, links[i])
		q := u.Query()
		if u.Host != w.host || u.Fragment != w.remark || q.Get("security") != w.security || q.Get("sni") != w.sni {
			t.Fatalf("link %d = %s, want %s %q security=%s sni=%q", i, links[i], w.host, w.remark, w.security, w.sni)
		}
	}
}

// The JSON and Clash outputs carry the same pair, from the inbound's own address
// or an externalProxy entry naming the front: the IPv6 dialled with the front's name first.
func TestFrontBothReachesJSONAndClash(t *testing.T) {
	const tlsStream = `"network":"tcp","security":"tls","tlsSettings":{"alpn":["h2"]}`
	client := model.Client{ID: testFrontUUID, Email: "user"}
	for _, shape := range []struct{ name, stream, address, remark string }{
		{"own address", `{` + tlsStream + `}`, testFrontName, "r-user"},
		{"externalProxy", `{` + tlsStream + `,"externalProxy":[{"forceTls":"same","dest":"` + testFrontName + `","port":443,"remark":"F"}]}`, "panel.example.com", "r-F-user"},
	} {
		t.Run(shape.name+"/json", func(t *testing.T) {
			s := frontRequest(t, "both")
			inbound := behindFront(model.VLESS, 443, frontVlessSettings, shape.stream, shape.address)
			js := NewSubJsonService("", "", "", "", nil)
			configs := js.clientConfigs(s, inbound, client, "sub.example.com", s.frontUnits(inbound, streamEndpoints(inbound)))
			assertJSONPair(t, configs, shape.remark)
		})
		t.Run(shape.name+"/clash", func(t *testing.T) {
			s := frontRequest(t, "both")
			inbound := behindFront(model.VLESS, 443, frontVlessSettings, shape.stream, shape.address)
			clash := &SubClashService{SubService: s}
			proxies := clash.clientProxies(s, inbound, client, "sub.example.com", s.frontUnits(inbound, streamEndpoints(inbound)))
			want := []struct{ server, servername, name string }{
				{testFrontIPv6, testFrontName, shape.remark + " · IPv6"},
				{testFrontName, "", shape.remark + " · IPv4"},
			}
			if len(proxies) != len(want) {
				t.Fatalf("proxies = %d, want %d", len(proxies), len(want))
			}
			for i, w := range want {
				servername, _ := proxies[i]["servername"].(string)
				if proxies[i]["server"] != w.server || servername != w.servername || proxies[i]["name"] != w.name {
					t.Fatalf("proxy %d = %v, want server %s servername %q name %q", i, proxies[i], w.server, w.servername, w.name)
				}
			}
		})
	}
}

func assertJSONPair(t *testing.T, configs []json_util.RawMessage, remark string) {
	t.Helper()
	want := []struct{ address, serverName, remarks string }{
		{"[" + testFrontIPv6 + "]", testFrontName, remark + " · IPv6"},
		{testFrontName, "", remark + " · IPv4"},
	}
	if len(configs) != len(want) {
		t.Fatalf("configs = %d, want %d", len(configs), len(want))
	}
	for i, w := range want {
		var doc struct {
			Remarks   string `json:"remarks"`
			Outbounds []struct {
				Settings       map[string]any `json:"settings"`
				StreamSettings struct {
					TLSSettings map[string]any `json:"tlsSettings"`
				} `json:"streamSettings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(configs[i], &doc); err != nil || len(doc.Outbounds) == 0 {
			t.Fatalf("config %d: %v\n%s", i, err, configs[i])
		}
		serverName, _ := doc.Outbounds[0].StreamSettings.TLSSettings["serverName"].(string)
		if doc.Outbounds[0].Settings["address"] != w.address || serverName != w.serverName || doc.Remarks != w.remarks {
			t.Fatalf("config %d dials %v with serverName %q named %q, want %s %q %q", i,
				doc.Outbounds[0].Settings["address"], serverName, doc.Remarks, w.address, w.serverName, w.remarks)
		}
	}
}

// AnyConnect refuses an IPv6 literal and cisco's certificate is for a name, so
// a cisco customer behind a front keeps the IPv4 address in every mode.
func TestFrontCiscoStaysOnTheName(t *testing.T) {
	s := frontRequest(t, "v6")
	inbound := behindFront(model.Cisco, 4443, `{}`, `{}`, testFrontName)
	if units := s.frontUnits(inbound, nil); units != nil {
		t.Fatalf("cisco got %d front renderings, want none", len(units))
	}
	login, ok := s.ciscoLoginFor(inbound, model.Client{Email: "user", Password: "pw"})
	if !ok || login.Server != testFrontName {
		t.Fatalf("cisco server = %q, want %q", login.Server, testFrontName)
	}
}

// OpenVPN keeps one file per client: "v6" points it at the IPv6, "both" adds
// the IPv6 as a remote tried ahead of the IPv4 one.
func TestFrontOpenVPNProfileCarriesTheIPv6(t *testing.T) {
	old := localCountryLabel
	localCountryLabel = func() string { return "" }
	t.Cleanup(func() { localCountryLabel = old })

	for _, tc := range []struct{ family, address, address6 string }{
		{"v6", "[" + testFrontIPv6 + "]", ""},
		{"both", testFrontName, "[" + testFrontIPv6 + "]"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			s := frontRequest(t, tc.family)
			profile, ok := s.openvpnProfileFor(behindFront(model.OpenVPN, 1194, `{}`, `{}`, testFrontName), model.Client{Email: "user", Password: "pw"})
			if !ok || profile.Address != tc.address || profile.Address6 != tc.address6 {
				t.Fatalf("profile remotes = %q then %q, want %q then %q", profile.Address6, profile.Address, tc.address6, tc.address)
			}
			// The file is the whole of it: no second IPv6 rendering beside it.
			if units := s.frontUnits(behindFront(model.OpenVPN, 1194, `{}`, `{}`, testFrontName), nil); units != nil {
				t.Fatalf("openvpn got %d front renderings beside its file, want none", len(units))
			}
		})
	}
}
