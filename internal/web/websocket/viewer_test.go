package websocket

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// ownedNames is a payload listing client emails; a scoped viewer's share is
// the emails it owns, or nothing when it owns none of them.
type ownedNames struct {
	Names []string `json:"names"`
	cuts  *int
}

func (p ownedNames) ClientEmails() []string { return p.Names }

func (p ownedNames) OwnedBy(owned Access) any {
	*p.cuts++
	var mine []string
	for _, name := range p.Names {
		if owned.Emails[name] {
			mine = append(mine, name)
		}
	}
	if mine == nil {
		return nil
	}
	return mine
}

// nextPayload reads the next message on the tab and returns its type and payload.
func nextPayload(t *testing.T, c *Client) (MessageType, string) {
	t.Helper()
	select {
	case raw := <-c.Send:
		var msg struct {
			Type    MessageType     `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("tab %s: %v", c.ID, err)
		}
		return msg.Type, string(msg.Payload)
	case <-time.After(time.Second):
		t.Fatalf("tab %s received nothing", c.ID)
		return "", ""
	}
}

type resolverCall struct {
	viewers []Viewer
	emails  []string
}

// One call resolves the viewers once for all its messages, asking only about the
// emails they name, and cuts one share per scoped admin however many tabs it has.
func TestHub_BroadcastOwnedSendsEachViewerItsShare(t *testing.T) {
	h := NewHub()
	defer h.Stop()
	go h.Run()

	boss := Viewer{AdminId: 1}
	resA := Viewer{AdminId: 7, Scoped: true}
	resB := Viewer{AdminId: 8, Scoped: true}
	stranger := Viewer{AdminId: 9, Scoped: true}
	unresolved := Viewer{AdminId: 10, Scoped: true}
	var calls []resolverCall
	h.SetAccessResolver(func(viewers []Viewer, emails []string) map[Viewer]*Access {
		calls = append(calls, resolverCall{viewers, emails})
		return map[Viewer]*Access{
			boss:       nil,
			unresolved: nil,
			resA:       {Emails: map[string]bool{"a@x": true, "c@x": true}},
			resB:       {Emails: map[string]bool{"z@x": true}},
		}
	})
	tabs := map[string]*Client{
		"boss":     NewViewerClient("boss", boss),
		"a-1":      NewViewerClient("a-1", resA),
		"a-2":      NewViewerClient("a-2", resA),
		"b":        NewViewerClient("b", resB),
		"stranger": NewViewerClient("stranger", stranger),
		// A scoped viewer the resolver vouches for without an Access owns nothing.
		"unresolved": NewViewerClient("unresolved", unresolved),
	}
	for _, c := range tabs {
		h.Register(c)
	}
	waitClientCount(t, h, len(tabs))

	cuts := 0
	h.BroadcastOwned(
		OwnedMessage{Type: MessageTypeTraffic, Payload: ownedNames{Names: []string{"a@x", "b@x"}, cuts: &cuts}},
		OwnedMessage{Type: MessageTypeClientStats, Payload: ownedNames{Names: []string{"c@x"}, cuts: &cuts}},
	)
	h.Broadcast(MessageTypeStatus, "after")

	if len(calls) != 1 || len(calls[0].viewers) != 5 {
		t.Fatalf("resolver calls = %v, want one call with the 5 distinct viewers", calls)
	}
	if !slices.Equal(calls[0].emails, []string{"a@x", "b@x", "c@x"}) {
		t.Fatalf("resolver asked about %v, want the emails the messages name", calls[0].emails)
	}
	if cuts != 4 {
		t.Fatalf("shares cut = %d, want one per message per scoped admin the resolver vouched for (4)", cuts)
	}
	want := map[string][]string{
		"boss": {`{"names":["a@x","b@x"]}`, `{"names":["c@x"]}`},
		"a-1":  {`["a@x"]`, `["c@x"]`},
		"a-2":  {`["a@x"]`, `["c@x"]`},
	}
	types := []MessageType{MessageTypeTraffic, MessageTypeClientStats}
	for name, c := range tabs {
		for i, wantPayload := range want[name] {
			if typ, payload := nextPayload(t, c); typ != types[i] || payload != wantPayload {
				t.Fatalf("%s message %d = %s %s, want %s %s", name, i, typ, payload, types[i], wantPayload)
			}
		}
		if typ, payload := nextPayload(t, c); typ != MessageTypeStatus {
			t.Fatalf("%s got %s %s before the status, want nothing", name, typ, payload)
		}
	}
}

// Until the panel service installs a resolver nobody is vouched for, so a
// message that lists clients must reach no one rather than everyone.
func TestHub_BroadcastOwnedWithoutResolverReachesNobody(t *testing.T) {
	h := NewHub()
	defer h.Stop()
	go h.Run()

	tab := NewViewerClient("boss", Viewer{AdminId: 1})
	h.Register(tab)
	waitClientCount(t, h, 1)

	cuts := 0
	h.BroadcastOwned(OwnedMessage{Type: MessageTypeTraffic, Payload: ownedNames{Names: []string{"a@x"}, cuts: &cuts}})
	h.Broadcast(MessageTypeStatus, "after")
	if typ, payload := nextPayload(t, tab); typ != MessageTypeStatus {
		t.Fatalf("got %s %s with no resolver installed", typ, payload)
	}
}

// Nodes and outbounds went out with Broadcast, so every reseller's socket got the
// node addresses and cert pins REST refuses them. Only live unscoped tabs may.
func TestHub_BroadcastUnscopedSkipsScopedAndDeadViewers(t *testing.T) {
	h := NewHub()
	defer h.Stop()
	go h.Run()

	boss := Viewer{AdminId: 1}
	manager := Viewer{AdminId: 2}
	signedOut := Viewer{AdminId: 3}
	reseller := Viewer{AdminId: 7, Scoped: true}
	var calls []resolverCall
	h.SetAccessResolver(func(viewers []Viewer, emails []string) map[Viewer]*Access {
		calls = append(calls, resolverCall{viewers, emails})
		return map[Viewer]*Access{boss: nil, manager: nil, reseller: {}}
	})
	tabs := map[string]*Client{
		"boss":       NewViewerClient("boss", boss),
		"manager":    NewViewerClient("manager", manager),
		"signed out": NewViewerClient("signed out", signedOut),
		"reseller":   NewViewerClient("reseller", reseller),
	}
	for _, c := range tabs {
		h.Register(c)
	}
	waitClientCount(t, h, len(tabs))

	h.BroadcastUnscoped(MessageTypeNodes, []string{"node-1"})
	h.Broadcast(MessageTypeStatus, "after")

	if len(calls) != 1 || len(calls[0].viewers) != 3 || slices.Contains(calls[0].viewers, reseller) {
		t.Fatalf("resolver calls = %v, want one call with the 3 unscoped viewers", calls)
	}
	for name, c := range tabs {
		if name == "boss" || name == "manager" {
			if typ, payload := nextPayload(t, c); typ != MessageTypeNodes || payload != `["node-1"]` {
				t.Fatalf("%s got %s %s, want the nodes", name, typ, payload)
			}
		}
		if typ, payload := nextPayload(t, c); typ != MessageTypeStatus {
			t.Fatalf("%s got %s %s before the status, want nothing", name, typ, payload)
		}
	}
}
