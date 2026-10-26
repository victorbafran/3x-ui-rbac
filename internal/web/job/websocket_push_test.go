package job

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The reseller "a" owns five clients; the push test lists each under one key only.
const (
	pushMine   = "a:mine@x"
	pushAlso   = "a:also@x"
	pushGuid   = "a:guid@x"
	pushIdle   = "a:idle@x"
	pushStats  = "a:stats@x"
	pushTheirs = "b:theirs@x"
	pushBoss   = "boss@x"
)

type pushHubServer struct{ hub *websocket.Hub }

func (s pushHubServer) GetCron() *cron.Cron     { return nil }
func (s pushHubServer) GetCtx() context.Context { return context.Background() }
func (s pushHubServer) GetWSHub() any           { return s.hub }

// openPanels opens one panel per kind of session on a hub wired as web.go wires it,
// retires three resellers' sessions, and returns the tabs and inbounds a, shared, boss, b.
func openPanels(t *testing.T) (map[string]*websocket.Client, map[string]int) {
	t.Helper()
	setupIntegrationDB(t)
	db := database.GetDB()
	var boss model.User
	if err := db.Where("role = ?", string(model.RoleSuperAdmin)).First(&boss).Error; err != nil {
		t.Fatalf("super admin: %v", err)
	}
	client := func(email string, owner int) {
		if err := db.Create(&model.ClientRecord{Email: email, UUID: email, Enable: true, AdminId: owner}).Error; err != nil {
			t.Fatalf("client %s: %v", email, err)
		}
	}
	admin := func(name string, role model.Role, owns ...string) model.User {
		u := model.User{Username: name, Password: "x", Role: role, Enabled: true}
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("admin %s: %v", name, err)
		}
		for _, email := range owns {
			client(email, u.Id)
		}
		return u
	}
	inbound := func(owner int, tag string, port int) int {
		ib := model.Inbound{UserId: owner, Tag: tag, Remark: tag, Protocol: model.VLESS, Port: port, Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}"}
		if err := db.Create(&ib).Error; err != nil {
			t.Fatalf("inbound %s: %v", tag, err)
		}
		return ib.Id
	}
	manager := admin("mgr", model.RoleManager)
	resA := admin("res-a", model.RoleReseller, pushMine, pushAlso, pushGuid, pushIdle, pushStats)
	resB := admin("res-b", model.RoleReseller, pushTheirs)
	off := admin("res-off", model.RoleReseller, "off@x")
	gone := admin("res-gone", model.RoleReseller, "gone@x")
	bumped := admin("res-bumped", model.RoleReseller, "bumped@x")
	client(pushBoss, 0)
	inbounds := map[string]int{
		"a":      inbound(resA.Id, "inbound-a", 40501),
		"shared": inbound(boss.Id, "inbound-shared", 40502),
		"boss":   inbound(boss.Id, "inbound-boss", 40503),
		"b":      inbound(resB.Id, "inbound-b", 40504),
	}
	if err := db.Create(&model.AdminInbound{AdminId: resA.Id, InboundId: inbounds["shared"]}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	panel.NewWebSocketService(hub)
	prev := global.GetWebServer()
	global.SetWebServer(pushHubServer{hub: hub})
	t.Cleanup(func() { global.SetWebServer(prev) })

	viewer := func(u model.User, scoped bool) websocket.Viewer {
		return websocket.Viewer{AdminId: u.Id, Scoped: scoped, LoginEpoch: u.LoginEpoch}
	}
	tabs := map[string]*websocket.Client{
		"super_admin":         websocket.NewViewerClient("super_admin", viewer(boss, false)),
		"manager":             websocket.NewViewerClient("manager", viewer(manager, false)),
		"reseller":            websocket.NewViewerClient("reseller", viewer(resA, true)),
		"reseller second tab": websocket.NewViewerClient("reseller second tab", viewer(resA, true)),
		"disabled":            websocket.NewViewerClient("disabled", viewer(off, true)),
		"deleted":             websocket.NewViewerClient("deleted", viewer(gone, true)),
		"epoch bumped":        websocket.NewViewerClient("epoch bumped", viewer(bumped, true)),
	}
	for _, c := range tabs {
		hub.Register(c)
	}
	for deadline := time.Now().Add(2 * time.Second); hub.GetClientCount() != len(tabs); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("panels never registered")
		}
	}

	if err := db.Model(&model.User{}).Where("id = ?", off.Id).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := db.Delete(&model.User{}, gone.Id).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", bumped.Id).Update("login_epoch", bumped.LoginEpoch+1).Error; err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	return tabs, inbounds
}

// untilSentinel returns the messages a panel received before the clients
// invalidate, which every panel gets after the pushes under test.
func untilSentinel(t *testing.T, c *websocket.Client) [][]byte {
	t.Helper()
	var got [][]byte
	for {
		select {
		case raw := <-c.Send:
			if bytes.Contains(raw, []byte(`"type":"invalidate","payload":{"type":"clients"}`)) {
				return got
			}
			got = append(got, raw)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no sentinel after %d messages", c.ID, len(got))
		}
	}
}

func payloadOf(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var msg struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("message %s: %v", raw, err)
	}
	return msg.Payload
}

func emailsOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var rows []struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("rows %s: %v", raw, err)
	}
	emails := make([]string, 0, len(rows))
	for _, r := range rows {
		emails = append(emails, r.Email)
	}
	return emails
}

func sameJSON(t *testing.T, got json.RawMessage, want any) bool {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	return string(got) == string(raw)
}

// Every live push reached every panel, so a reseller's browser held other admins'
// customers, inbound traffic, outbounds and node addresses, all of which REST refuses.
func TestLivePushesShowAResellerOnlyTheirOwnClients(t *testing.T) {
	tabs, inbound := openPanels(t)
	rows := []*xray.ClientTraffic{
		{Email: pushMine, Up: 10, Down: 20}, {Email: pushTheirs, Up: 30}, {Email: pushBoss, Down: 40},
	}
	// The second outbound shares an owned inbound's tag: only IsInbound keeps it out.
	traffics := []*xray.Traffic{
		{IsInbound: true, Tag: "inbound-a", Up: 5, Down: 6},
		{IsInbound: true, Tag: "inbound-boss", Up: 7},
		{IsOutbound: true, Tag: "direct", Up: 8},
		{IsOutbound: true, Tag: "inbound-a", Up: 9},
	}
	activeInbounds := map[string][]string{"local-guid": {"inbound-a", "inbound-boss"}, "node-guid": {"inbound-b"}}
	nodeTraffics := []*xray.Traffic{{IsInbound: true, Tag: "inbound-shared", Up: 7}, {IsInbound: true, Tag: "inbound-b", Up: 3}}
	summary := []service.InboundTrafficSummary{
		{Id: inbound["a"], Up: 5, Enable: true}, {Id: inbound["boss"], Up: 6}, {Id: inbound["shared"], Down: 7},
	}
	trafficMsg := trafficPush{
		"traffics":       traffics,
		"clientTraffics": rows,
		"onlineClients":  []string{pushAlso, pushTheirs, pushBoss},
		"onlineByGuid":   map[string][]string{"local-guid": {pushGuid, pushBoss}, "node-guid": {pushTheirs}},
		"activeInbounds": activeInbounds,
		"lastOnlineMap":  map[string]int64{pushIdle: 1, pushTheirs: 3, pushBoss: 4},
		"nodeTraffics":   nodeTraffics,
	}
	// A snapshot also lists idle clients, which the traffic message does not name.
	statsRows := append(slices.Clone(rows), &xray.ClientTraffic{Email: pushStats})
	statsMsg := clientStatsPush{"snapshot": true, "clients": statsRows, "inbounds": summary}
	nodes := []map[string]any{{"address": "10.9.9.9", "pinnedCertSha256": "ab12"}}
	outbounds := []map[string]any{{"tag": "exit-wgr9", "up": 1}}

	websocket.BroadcastTraffic(trafficMsg, statsMsg)
	broadcastSidecarSpeed("cisco", rows, 10*time.Second)
	websocket.BroadcastNodes(nodes)
	websocket.BroadcastOutbounds(outbounds)
	websocket.BroadcastInvalidate(websocket.MessageTypeClients)

	sent := []struct {
		typ     websocket.MessageType
		payload any
	}{
		{websocket.MessageTypeTraffic, trafficMsg},
		{websocket.MessageTypeClientStats, statsMsg},
		{websocket.MessageTypeTraffic, sidecarSpeedPayload("cisco", rows, 10*time.Second)},
		{websocket.MessageTypeNodes, nodes},
		{websocket.MessageTypeOutbounds, outbounds},
	}
	for _, name := range []string{"super_admin", "manager"} {
		t.Run(name+" receives the bytes it always did", func(t *testing.T) {
			got := untilSentinel(t, tabs[name])
			if len(got) != len(sent) {
				t.Fatalf("got %d messages, want %d", len(got), len(sent))
			}
			for i, raw := range got {
				var stamp struct {
					Time int64 `json:"time"`
				}
				if err := json.Unmarshal(raw, &stamp); err != nil {
					t.Fatal(err)
				}
				want, err := json.Marshal(websocket.Message{Type: sent[i].typ, Payload: sent[i].payload, Time: stamp.Time})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, want) {
					t.Fatalf("message %d:\n got %s\nwant %s", i, raw, want)
				}
			}
		})
	}

	for _, name := range []string{"reseller", "reseller second tab"} {
		t.Run(name+" sees only their own clients and inbounds", func(t *testing.T) {
			got := untilSentinel(t, tabs[name])
			if len(got) != 3 {
				t.Fatalf("got %d messages, want traffic, client_stats and cisco traffic, no nodes or outbounds", len(got))
			}
			for _, raw := range got {
				for _, foreign := range []string{pushTheirs, pushBoss, "10.9.9.9", "exit-wgr9", `"direct"`} {
					if strings.Contains(string(raw), foreign) {
						t.Fatalf("%s reached the reseller: %s", foreign, raw)
					}
				}
			}

			traffic := payloadOf(t, got[0])
			if e := emailsOf(t, traffic["clientTraffics"]); !slices.Equal(e, []string{pushMine}) {
				t.Errorf("clientTraffics = %v, want only %s", e, pushMine)
			}
			for key, want := range map[string]any{
				"onlineClients":  []string{pushAlso},
				"onlineByGuid":   map[string][]string{"local-guid": {pushGuid}, "node-guid": {}},
				"lastOnlineMap":  map[string]int64{pushIdle: 1},
				"traffics":       []*xray.Traffic{traffics[0]},
				"activeInbounds": map[string][]string{"local-guid": {"inbound-a"}, "node-guid": {}},
				"nodeTraffics":   []*xray.Traffic{nodeTraffics[0]},
			} {
				if !sameJSON(t, traffic[key], want) {
					t.Errorf("%s = %s", key, traffic[key])
				}
			}
			if len(traffic) != len(trafficMsg) {
				t.Errorf("traffic keys = %d, want the %d the job sent", len(traffic), len(trafficMsg))
			}

			stats := payloadOf(t, got[1])
			if e := emailsOf(t, stats["clients"]); !slices.Equal(e, []string{pushMine, pushStats}) {
				t.Errorf("client_stats clients = %v, want only %s and %s", e, pushMine, pushStats)
			}
			if string(stats["snapshot"]) != "true" || !sameJSON(t, stats["inbounds"], []service.InboundTrafficSummary{summary[0], summary[2]}) {
				t.Errorf("client_stats snapshot/inbounds = %s/%s", stats["snapshot"], stats["inbounds"])
			}

			speed := payloadOf(t, got[2])
			if e := emailsOf(t, speed["clientTraffics"]); !slices.Equal(e, []string{pushMine}) {
				t.Errorf("cisco clientTraffics = %v, want only %s", e, pushMine)
			}
			if string(speed["clientTrafficSource"]) != `"cisco"` || string(speed["clientTrafficIntervalMs"]) != "10000" {
				t.Errorf("cisco source/interval = %s/%s", speed["clientTrafficSource"], speed["clientTrafficIntervalMs"])
			}
		})
	}

	for _, name := range []string{"disabled", "deleted", "epoch bumped"} {
		t.Run(name+" admin's panel is shown nothing", func(t *testing.T) {
			if got := untilSentinel(t, tabs[name]); len(got) != 0 {
				t.Fatalf("got %d messages, first %s", len(got), got[0])
			}
		})
	}
}

// A key added to a push later is not known to name no client, so a reseller
// must not receive it until someone decides; the bare snapshot flag says nothing.
func TestOwnedSharesLeaveOutWhatTheyDoNotKnow(t *testing.T) {
	owned := websocket.Access{Emails: map[string]bool{pushMine: true}, InboundTags: map[string]bool{"inbound-a": true}}
	share, ok := trafficPush{"futureKey": []string{pushTheirs}, "onlineClients": []string{pushMine}}.OwnedBy(owned).(map[string]any)
	if !ok || len(share) != 1 || share["futureKey"] != nil {
		t.Fatalf("traffic share = %#v, want only onlineClients", share)
	}
	if got := (clientStatsPush{"snapshot": true, "clients": []*xray.ClientTraffic{{Email: pushTheirs}}}).OwnedBy(owned); got != nil {
		t.Fatalf("client_stats share = %#v, want nothing when no row is the reseller's", got)
	}
	// A failed node query sends null, which leaves the page's speeds as they were;
	// an empty list would wipe them.
	nodeShare := trafficPush{"nodeTraffics": []*xray.Traffic(nil)}.OwnedBy(owned)
	if raw, err := json.Marshal(nodeShare); err != nil || string(raw) != `{"nodeTraffics":null}` {
		t.Fatalf("node traffic share = %s %v, want null kept", raw, err)
	}
}
