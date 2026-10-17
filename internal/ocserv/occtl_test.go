package ocserv

import "testing"

// realOcctlOutput is a verbatim capture from `occtl -j show users` on ocserv
// 1.2.4 with two clients connected. It is kept whole rather than trimmed to a
// handful of fields because the byte counters arrive as JSON strings while the
// session id arrives as a number — a shape a hand-written fixture would very
// plausibly get wrong in exactly the direction that hides a bug.
const realOcctlOutput = `[
  {
    "ID":  92145,
    "Username":  "beta",
    "Groupname":  "(none)",
    "State":  "connected",
    "vhost":  "default",
    "Device":  "vpns1",
    "MTU":  "1434",
    "Remote IP":  "127.0.0.1",
    "IPv4":  "10.77.0.48",
    "User-Agent":  "Open AnyConnect VPN Agent v9.12",
    "RX":  "1024",
    "TX":  "400",
    "_RX":  "1 KB",
    "_TX":  "400 bytes",
    "Average RX":  "0 bytes/s",
    "DPD":  "60",
    "Connected at":  "2026-10-17 10:47",
    "raw_connected_at": 1792234068,
    "Session":  "Mp8QFQ",
    "DNS":	["1.1.1.1"],
    "Routes":	[],
    "Restricted to routes":  "False"
  },
  {
    "ID":  92128,
    "Username":  "alpha",
    "Groupname":  "(none)",
    "State":  "connected",
    "vhost":  "default",
    "Device":  "vpns1",
    "IPv4":  "10.77.0.12",
    "RX":  "0",
    "TX":  "448",
    "Session":  "A1b2C3",
    "DNS":	["1.1.1.1"],
    "Routes":	[]
  }
]`

func TestParseSessionsReadsRealOcctlOutput(t *testing.T) {
	sessions, err := parseSessions([]byte(realOcctlOutput))
	if err != nil {
		t.Fatalf("parseSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	first := sessions[0]
	if first.ID != 92145 || first.Username != "beta" {
		t.Fatalf("first session = %+v, want id 92145 for beta", first)
	}
	if first.RX != 1024 || first.TX != 400 {
		t.Fatalf("counters = rx %d tx %d, want 1024/400 — the JSON holds them as strings", first.RX, first.TX)
	}
	if first.State != "connected" || first.IPv4 != "10.77.0.48" {
		t.Fatalf("state/address = %q/%q", first.State, first.IPv4)
	}
}

// An empty user list is what occtl answers when the server is up and idle. It
// must read as "nobody is connected", never as a failure, or every quiet poll
// would look like a broken instance.
func TestParseSessionsAcceptsAnEmptyList(t *testing.T) {
	sessions, err := parseSessions([]byte("[\n]"))
	if err != nil {
		t.Fatalf("empty list should parse: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions from an empty list", len(sessions))
	}
}

func TestParseSessionsRejectsGarbage(t *testing.T) {
	if _, err := parseSessions([]byte("could not send message; server is offline")); err == nil {
		t.Fatal("an occtl error message parsed as a session list")
	}
}
