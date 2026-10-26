package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	ws "github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
)

// emailList is a payload of client emails; a scoped viewer's share is the
// emails its admin owns.
type emailList []string

func (l emailList) ClientEmails() []string { return l }

func (l emailList) OwnedBy(owned websocket.Access) any {
	mine := []string{}
	for _, email := range l {
		if owned.Emails[email] {
			mine = append(mine, email)
		}
	}
	return mine
}

// The upgrade only asked whether someone was signed in, so a reseller's socket
// and a manager's were the same socket and were handed the same client lists.
func TestWebSocketUpgradeScopesTheSocketLikeTheRESTListings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	db := database.GetDB()
	reseller := seedReseller(t, "ws-res")
	// A socket that dropped the session's epoch would count as signed out.
	if err := db.Model(&model.User{}).Where("id = ?", reseller).Update("login_epoch", 3).Error; err != nil {
		t.Fatalf("epoch: %v", err)
	}
	manager := model.User{Username: "ws-mgr", Password: "x", Role: model.RoleManager, Enabled: true}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	disabled := seedReseller(t, "ws-off")
	if err := db.Model(&model.User{}).Where("id = ?", disabled).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	for email, owner := range map[string]int{"ws:mine@x": reseller, "ws:boss@x": 0} {
		if err := db.Create(&model.ClientRecord{Email: email, UUID: email, Enable: true, AdminId: owner}).Error; err != nil {
			t.Fatalf("client %s: %v", email, err)
		}
	}

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		var u model.User
		if database.GetDB().First(&u, c.GetHeader("X-Test-Admin")).Error == nil {
			session.SetAPIAuthUser(c, &u)
		}
		c.Next()
	})
	engine.GET("/ws", NewWebSocketController(panel.NewWebSocketService(hub)).HandleWebSocket)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	dial := func(id int) (*ws.Conn, *http.Response, error) {
		header := http.Header{"X-Test-Admin": {strconv.Itoa(id)}}
		return ws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", header)
	}

	if conn, resp, err := dial(disabled); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("a disabled admin's upgrade: err=%v resp=%v, want 403", err, resp)
	}

	sockets := map[string]*ws.Conn{}
	for name, id := range map[string]int{"reseller": reseller, "manager": manager.Id} {
		conn, _, err := dial(id)
		if err != nil {
			t.Fatalf("%s upgrade: %v", name, err)
		}
		t.Cleanup(func() { conn.Close() })
		sockets[name] = conn
	}
	for deadline := time.Now().Add(2 * time.Second); hub.GetClientCount() != 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("sockets never registered")
		}
	}

	hub.BroadcastOwned(websocket.OwnedMessage{Type: websocket.MessageTypeTraffic, Payload: emailList{"ws:mine@x", "ws:boss@x"}})
	want := map[string]string{"reseller": `["ws:mine@x"]`, "manager": `["ws:mine@x","ws:boss@x"]`}
	for name, conn := range sockets {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("%s read: %v", name, err)
		}
		var msg struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("%s message %s: %v", name, raw, err)
		}
		if string(msg.Payload) != want[name] {
			t.Fatalf("%s socket got %s, want %s", name, msg.Payload, want[name])
		}
	}
}
