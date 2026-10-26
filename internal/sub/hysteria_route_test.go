package sub

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestHostHysteriaAuth(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef"
	exit := func(route string) map[string]any { return map[string]any{"exit": "wgr9", "vlessRoute": route} }
	tests := []struct {
		name, auth string
		ep         map[string]any
		want       string
	}{
		{"32 hex stays without dashes", hex, exit("9"), "0123456789ab00090123456789abcdef"},
		{"a dashed UUID keeps its dashes", "01234567-89ab-4def-8123-456789abcdef", exit("443"), "01234567-89ab-01bb-8123-456789abcdef"},
		{"capitals outside the route are kept", "0123456789ABCDEF0123456789ABCDEF", exit("65535"), "0123456789ABffff0123456789ABCDEF"},
		{"a host without an exit keeps the auth whole", hex, map[string]any{"vlessRoute": "9"}, hex},
		{"a route out of range is not written", hex, exit("65536"), hex},
		{"32 characters that are not hex", "0123456789abcdef0123456789abcdeg", exit("9"), "0123456789abcdef0123456789abcdeg"},
		{"a UUID in braces is no spelling a route keeps", "{01234567-89ab-4def-8123-456789abcdef}", exit("9"), "{01234567-89ab-4def-8123-456789abcdef}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostHysteriaAuth(tt.auth, tt.ep); got != tt.want {
				t.Fatalf("hostHysteriaAuth(%q, %v) = %q, want %q", tt.auth, tt.ep, got, tt.want)
			}
		})
	}
}

const hyRouteAuth = "0123456789abcdef0123456789abcdef"

// seedHysteriaExitInbound is a Hysteria inbound with one client, three hosts: one
// leaving through exit wgr9 on route 9, one with a route of its own, one plain.
func seedHysteriaExitInbound(t *testing.T) {
	t.Helper()
	seedSubDB(t)
	db := database.GetDB()
	settings := fmt.Sprintf(`{"version":2,"clients":[{"auth":%q,"email":"hy@e","subId":"s1","enable":true}]}`, hyRouteAuth)
	ib := &model.Inbound{
		UserId: 1, Tag: "hy", Enable: true, Listen: "203.0.113.5", Port: 8443, Protocol: model.Hysteria, Remark: "hy",
		Settings: settings, StreamSettings: `{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"hy.sni"}}`,
	}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	rec := &model.ClientRecord{Email: "hy@e", SubID: "s1", Auth: hyRouteAuth, Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatal(err)
	}
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 1, Remark: "NL", Address: "a.example.ir", Port: 8443, Exit: "wgr9", VlessRoute: "9"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 2, Remark: "R", Address: "b.example.ir", Port: 8443, VlessRoute: "443"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 3, Remark: "P", Address: "c.example.ir", Port: 8443})
}

const hyRoutedAuth = "0123456789ab00090123456789abcdef"

// Only the host with an exit routes the auth: a route without an exit was never
// written into a Hysteria auth, and those links stay as they were.
func TestSub_HysteriaExitRoutesTheAuth_Raw(t *testing.T) {
	seedHysteriaExitInbound(t)
	links, _, _, _, err := NewSubService("").GetSubs("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.Join(links, "\n"), "\n")
	want := []string{
		"hysteria2://" + hyRoutedAuth + "@a.example.ir:8443",
		"hysteria2://" + hyRouteAuth + "@b.example.ir:8443",
		"hysteria2://" + hyRouteAuth + "@c.example.ir:8443",
	}
	if len(parts) != len(want) {
		t.Fatalf("links %v; want three", parts)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(parts[i], prefix+"?") {
			t.Errorf("link %d = %s, want it to start with %s", i, parts[i], prefix)
		}
	}
}

func TestSub_HysteriaExitRoutesTheAuth_JSON(t *testing.T) {
	seedHysteriaExitInbound(t)
	out, _, err := NewSubJsonService("", "", "", "", NewSubService("")).GetJson("s1", "req.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, `"auth": "`+hyRoutedAuth+`"`); got != 1 {
		t.Errorf("routed auth appears %d times, want once, in the exit host's config:\n%s", got, out)
	}
	if got := strings.Count(out, `"auth": "`+hyRouteAuth+`"`); got != 2 {
		t.Errorf("plain auth appears %d times, want in the two hosts without an exit:\n%s", got, out)
	}
}

func TestSub_HysteriaExitRoutesTheAuth_Clash(t *testing.T) {
	seedHysteriaExitInbound(t)
	yaml, _, err := NewSubClashService(false, "", false, NewSubService("")).GetClash("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(yaml, "password: "+hyRoutedAuth); got != 1 {
		t.Errorf("routed password appears %d times, want once, in the exit host's proxy:\n%s", got, yaml)
	}
	if got := strings.Count(yaml, "password: "+hyRouteAuth); got != 2 {
		t.Errorf("plain password appears %d times, want in the two hosts without an exit:\n%s", got, yaml)
	}
}
