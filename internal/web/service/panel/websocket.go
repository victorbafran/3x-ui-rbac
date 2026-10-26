// Package service: WebSocketService owns the per-connection pump goroutines
// and bridges the HTTP-layer controller to the broadcast hub. The controller
// handles the upgrade handshake and authentication, then hands the raw
// connection to this service which takes ownership of its lifecycle.
package panel

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"

	"github.com/google/uuid"
	ws "github.com/gorilla/websocket"
)

const (
	wsWriteWait       = 10 * time.Second
	wsPongWait        = 60 * time.Second
	wsPingPeriod      = (wsPongWait * 9) / 10
	wsClientReadLimit = 512
)

// WebSocketService manages WebSocket client connections. It owns the
// read/write pumps for each accepted connection and registers/unregisters
// clients with the hub.
type WebSocketService struct {
	hub *websocket.Hub
}

// NewWebSocketService creates a service backed by the given hub and tells the
// hub how to decide which clients each open panel may see.
func NewWebSocketService(hub *websocket.Hub) *WebSocketService {
	hub.SetAccessResolver(viewerAccess)
	return &WebSocketService{hub: hub}
}

// HandleConnection takes ownership of an upgraded WebSocket connection:
// registers a new client, starts the read/write pumps, and returns
// immediately. The connection is closed when both pumps exit.
func (s *WebSocketService) HandleConnection(conn *ws.Conn, remoteIP string, viewer websocket.Viewer) {
	if s == nil || s.hub == nil || conn == nil {
		if conn != nil {
			conn.Close()
		}
		return
	}

	client := websocket.NewViewerClient(uuid.New().String(), viewer)
	s.hub.Register(client)
	logger.Debugf("WebSocket client %s registered from %s", client.ID, remoteIP)

	go s.writePump(client, conn)
	go s.readPump(client, conn)
}

// viewerAccess applies the REST session rule at broadcast time: a viewer counts
// while its admin exists, is enabled and still has the login epoch it upgraded with.
func viewerAccess(viewers []websocket.Viewer, emails []string) map[websocket.Viewer]*websocket.Access {
	db := database.GetDB()
	if db == nil || len(viewers) == 0 {
		return nil
	}
	ids := make([]int, 0, len(viewers))
	for _, v := range viewers {
		ids = append(ids, v.AdminId)
	}
	var admins []model.User
	if err := db.Model(&model.User{}).Select("id", "enabled", "login_epoch").Where("id IN ?", ids).Find(&admins).Error; err != nil {
		logger.Warning("websocket: load viewer admins failed:", err)
		return nil
	}
	byId := make(map[int]model.User, len(admins))
	for _, a := range admins {
		byId[a.Id] = a
	}

	access := make(map[websocket.Viewer]*websocket.Access, len(viewers))
	var scoped []websocket.Viewer
	var scopedIds []int
	for _, v := range viewers {
		admin, ok := byId[v.AdminId]
		if !ok || !admin.Enabled || admin.LoginEpoch != v.LoginEpoch {
			continue
		}
		if !v.Scoped {
			access[v] = nil
			continue
		}
		// Only the viewer with the admin's current epoch gets here: one per admin.
		scoped = append(scoped, v)
		scopedIds = append(scopedIds, v.AdminId)
	}
	if len(scoped) == 0 {
		return access
	}

	clients := service.ClientService{}
	emailsOf, err := clients.OwnedEmailsAmong(scopedIds, emails)
	if err != nil {
		logger.Warning("websocket: load owned clients failed:", err)
		return access
	}
	inboundsOf, err := clients.OwnedInboundsByAdmin(scopedIds)
	if err != nil {
		logger.Warning("websocket: load owned inbounds failed:", err)
		return access
	}
	for _, v := range scoped {
		owned := &websocket.Access{
			Emails:      emailsOf[v.AdminId],
			InboundTags: make(map[string]bool, len(inboundsOf[v.AdminId])),
			InboundIds:  make(map[int]bool, len(inboundsOf[v.AdminId])),
		}
		for id, tag := range inboundsOf[v.AdminId] {
			owned.InboundIds[id] = true
			owned.InboundTags[tag] = true
		}
		access[v] = owned
	}
	return access
}

// readPump consumes inbound frames so the gorilla deadline/pong machinery keeps
// running. Clients send no commands today; frames are discarded.
func (s *WebSocketService) readPump(client *websocket.Client, conn *ws.Conn) {
	defer func() {
		if r := common.Recover("WebSocket readPump panic"); r != nil {
			logger.Error("WebSocket readPump panic recovered:", r)
		}
		s.hub.Unregister(client)
		conn.Close()
	}()

	conn.SetReadLimit(wsClientReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				logger.Debugf("WebSocket read error for client %s: %v", client.ID, err)
			}
			return
		}
	}
}

// writePump pushes hub messages to the connection and emits keepalive pings.
func (s *WebSocketService) writePump(client *websocket.Client, conn *ws.Conn) {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		if r := common.Recover("WebSocket writePump panic"); r != nil {
			logger.Error("WebSocket writePump panic recovered:", r)
		}
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case msg, ok := <-client.Send:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				_ = conn.WriteMessage(ws.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(ws.TextMessage, msg); err != nil {
				logger.Debugf("WebSocket write error for client %s: %v", client.ID, err)
				return
			}

		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(ws.PingMessage, nil); err != nil {
				logger.Debugf("WebSocket ping error for client %s: %v", client.ID, err)
				return
			}
		}
	}
}
