package websocket

import "time"

// Viewer is the admin a connection shows data to, fixed when the session
// upgraded. A scoped viewer is shown only the clients its admin owns.
type Viewer struct {
	AdminId    int
	Scoped     bool
	LoginEpoch int64
}

// Access is what a scoped viewer's admin owns among the names a broadcast carries:
// clients by email, and inbounds by tag and by id.
type Access struct {
	Emails      map[string]bool
	InboundTags map[string]bool
	InboundIds  map[int]bool
}

// OwnedPayload is a payload that names clients or inbounds. ClientEmails bounds the
// ownership lookup; OwnedBy returns a scoped viewer's share, or nil for none.
type OwnedPayload interface {
	ClientEmails() []string
	OwnedBy(owned Access) any
}

// OwnedMessage is one message of a BroadcastOwned call.
type OwnedMessage struct {
	Type    MessageType
	Payload OwnedPayload
}

// AccessResolver maps each viewer whose session is still valid to what its admin owns
// among emails, nil for an unscoped viewer. A viewer it leaves out is sent nothing.
type AccessResolver func(viewers []Viewer, emails []string) map[Viewer]*Access

// SetAccessResolver installs how the hub learns what each viewer may see.
// Until one is set, messages that list clients reach nobody.
func (h *Hub) SetAccessResolver(resolve AccessResolver) {
	if h != nil && resolve != nil {
		h.access.Store(&resolve)
	}
}

// outgoing is one queued message: the same bytes for every connection, or one
// rendering per viewer, where a viewer without one receives nothing.
type outgoing struct {
	all       []byte
	perViewer map[Viewer][]byte
}

func (o outgoing) bytesFor(v Viewer) []byte {
	if o.perViewer != nil {
		return o.perViewer[v]
	}
	return o.all
}

// BroadcastOwned queues messages that name clients or inbounds: an unscoped viewer gets
// the bytes Broadcast would send, a scoped one its admin's share. One lookup per call.
func (h *Hub) BroadcastOwned(msgs ...OwnedMessage) {
	if h == nil || h.GetClientCount() == 0 {
		return
	}
	pending := make([]OwnedMessage, 0, len(msgs))
	var emails []string
	for _, m := range msgs {
		if m.Payload == nil || h.shouldThrottle(m.Type) {
			continue
		}
		pending = append(pending, m)
		emails = append(emails, m.Payload.ClientEmails()...)
	}
	if len(pending) == 0 {
		return
	}
	viewers := h.connectedViewers()
	access := h.resolveAccess(viewers, emails)
	now := time.Now().UnixMilli()
	for _, m := range pending {
		perViewer := make(map[Viewer][]byte, len(access))
		var full []byte
		for _, v := range viewers {
			owned, live := access[v]
			if !live {
				continue
			}
			if !v.Scoped {
				if full == nil {
					full = render(m.Type, m.Payload, now)
				}
				perViewer[v] = full
				continue
			}
			if owned == nil {
				continue
			}
			// Viewers are distinct and a live admin has one epoch: one share per admin.
			if share := m.Payload.OwnedBy(*owned); share != nil {
				perViewer[v] = render(m.Type, share, now)
			}
		}
		if len(perViewer) > 0 {
			h.enqueue(outgoing{perViewer: perViewer})
		}
	}
}

// BroadcastUnscoped queues a message for unscoped viewers only: the node and outbound
// lists, which REST closes to resellers, would hand them every admin's setup.
func (h *Hub) BroadcastUnscoped(messageType MessageType, payload any) {
	if h == nil || payload == nil || h.GetClientCount() == 0 || h.shouldThrottle(messageType) {
		return
	}
	var unscoped []Viewer
	for _, v := range h.connectedViewers() {
		if !v.Scoped {
			unscoped = append(unscoped, v)
		}
	}
	access := h.resolveAccess(unscoped, nil)
	perViewer := make(map[Viewer][]byte, len(access))
	var data []byte
	for _, v := range unscoped {
		if _, live := access[v]; !live {
			continue
		}
		if data == nil {
			data = render(messageType, payload, time.Now().UnixMilli())
		}
		perViewer[v] = data
	}
	if len(perViewer) > 0 {
		h.enqueue(outgoing{perViewer: perViewer})
	}
}

// connectedViewers lists each distinct viewer among the open connections.
func (h *Hub) connectedViewers() []Viewer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[Viewer]bool, len(h.clients))
	viewers := make([]Viewer, 0, len(h.clients))
	for c := range h.clients {
		if !seen[c.viewer] {
			seen[c.viewer] = true
			viewers = append(viewers, c.viewer)
		}
	}
	return viewers
}

func (h *Hub) resolveAccess(viewers []Viewer, emails []string) map[Viewer]*Access {
	resolve := h.access.Load()
	if resolve == nil || len(viewers) == 0 {
		return nil
	}
	return (*resolve)(viewers, emails)
}
