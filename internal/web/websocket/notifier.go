// Package websocket provides WebSocket hub for real-time updates and notifications.
package websocket

import (
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// GetHub returns the global WebSocket hub instance.
func GetHub() *Hub {
	webServer := global.GetWebServer()
	if webServer == nil {
		return nil
	}
	hub := webServer.GetWSHub()
	if hub == nil {
		return nil
	}
	wsHub, ok := hub.(*Hub)
	if !ok {
		logger.Warning("WebSocket hub type assertion failed")
		return nil
	}
	return wsHub
}

// HasClients returns true if any WebSocket client is connected.
// Use this to skip expensive work (DB queries, serialization) when no browser is open.
func HasClients() bool {
	hub := GetHub()
	return hub != nil && hub.GetClientCount() > 0
}

// BroadcastStatus broadcasts server status update to all connected clients.
func BroadcastStatus(status any) {
	if hub := GetHub(); hub != nil {
		hub.Broadcast(MessageTypeStatus, status)
	}
}

// BroadcastTraffic pushes traffic counters and online lists, then the tick's client
// stats unless stats is nil. A scoped viewer gets its share; one lookup serves both.
func BroadcastTraffic(traffic, stats OwnedPayload) {
	if hub := GetHub(); hub != nil {
		hub.BroadcastOwned(
			OwnedMessage{Type: MessageTypeTraffic, Payload: traffic},
			OwnedMessage{Type: MessageTypeClientStats, Payload: stats},
		)
	}
}

// BroadcastNodes pushes the node list, addresses and cert pins included, to
// unscoped panels only: REST answers a reseller's node list with nothing.
func BroadcastNodes(nodes any) {
	if hub := GetHub(); hub != nil {
		hub.BroadcastUnscoped(MessageTypeNodes, nodes)
	}
}

// BroadcastOutbounds pushes the outbound list to unscoped panels only: REST
// refuses a reseller every outbound and its traffic.
func BroadcastOutbounds(outbounds any) {
	if hub := GetHub(); hub != nil {
		hub.BroadcastUnscoped(MessageTypeOutbounds, outbounds)
	}
}

// BroadcastNotification broadcasts a system notification to all connected clients.
func BroadcastNotification(title, message, level string) {
	hub := GetHub()
	if hub == nil {
		return
	}
	hub.Broadcast(MessageTypeNotification, map[string]string{
		"title":   title,
		"message": message,
		"level":   level,
	})
}

// BroadcastXrayState broadcasts Xray state change to all connected clients.
func BroadcastXrayState(state string, errorMsg string) {
	hub := GetHub()
	if hub == nil {
		return
	}
	hub.Broadcast(MessageTypeXrayState, map[string]string{
		"state":    state,
		"errorMsg": errorMsg,
	})
}

// BroadcastInvalidate sends a lightweight signal telling clients to re-fetch
// the named data type via REST. Use this when the caller already knows the
// payload is too large to push directly (e.g., 10k+ clients) to skip the
// JSON-marshal cost on the hot path.
func BroadcastInvalidate(dataType MessageType) {
	if hub := GetHub(); hub != nil {
		hub.broadcastInvalidate(dataType)
	}
}
