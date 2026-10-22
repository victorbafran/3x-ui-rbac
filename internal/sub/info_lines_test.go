package sub

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func seedInfoLinesClient(t *testing.T) {
	t.Helper()
	setupInfoNodeTestDB(t)
	db := database.GetDB()
	for _, row := range []any{
		&model.Inbound{
			Id: 1, UserId: 1, Remark: "TUN", Enable: true, Port: 443, Protocol: model.VLESS,
			Settings:       `{"clients":[{"id":"c1-uuid","email":"ali","subId":"sub-lines","enable":true,"totalGB":10737418240}]}`,
			StreamSettings: `{"network":"tcp","security":"none"}`,
		},
		&model.ClientRecord{Id: 1, Email: "ali", SubID: "sub-lines", UUID: "c1-uuid", Enable: true, TotalGB: 10737418240},
		&model.ClientInbound{InboundId: 1, ClientId: 1},
		&xray.ClientTraffic{InboundId: 1, Email: "ali", Up: 1073741824, Down: 1073741824, Total: 10737418240, Enable: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// Each line is an entry of its own, on its own port, since some apps fold
// entries sharing an address; a line with only an unlimited value is left out.
func TestEachInfoLineIsAnEntryOfItsOwn(t *testing.T) {
	seedInfoLinesClient(t)
	// Every request rereads its settings, so a field set on the service alone
	// would be overwritten by the stored value.
	lines := "👤 {{EMAIL}}\r\n⏳ {{DAYS_LEFT}} days\n\n📶 {{TRAFFIC_LEFT}}"
	if err := database.GetDB().Create(&model.Setting{Key: "subInfoLines", Value: lines}).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewSubService("{{INBOUND}}")
	svc.subInfoNodeEnable = true
	svc.subscriptionBody = true

	links, _, _, _, err := svc.GetSubs("sub-lines", "sub.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"socks://127.0.0.1:1080#👤 ali", "socks://127.0.0.1:1081#📶 8.00GB"}
	if len(links) != 3 {
		t.Fatalf("links %q; want two info entries and the config", links)
	}
	for i, w := range want {
		got, _ := url.QueryUnescape(links[i])
		if got != w {
			t.Errorf("entry %d = %q, want %q", i, got, w)
		}
	}
	if !strings.HasPrefix(links[2], "vless://") {
		t.Errorf("the config is not after the info entries: %q", links[2])
	}
}

// Clash keeps every info entry out of its proxy groups, not only the first.
func TestEveryInfoEntryIsADummyToClash(t *testing.T) {
	for port := infoBasePort; port < infoBasePort+maxInfoLines; port++ {
		if !isDummyProxy(map[string]any{"type": "socks5", "server": "127.0.0.1", "port": port}) {
			t.Errorf("info entry on %d would join the proxy groups", port)
		}
	}
	if isDummyProxy(map[string]any{"type": "socks5", "server": "127.0.0.1", "port": infoBasePort + maxInfoLines}) {
		t.Error("a real local socks proxy past the info range was taken for an info entry")
	}
}
