package openvpn

import (
	"strings"
	"testing"
	"time"
)

// A real status file, as OpenVPN 2.6 writes it with status-version 2.
const sampleStatus = `TITLE,OpenVPN 2.6.12 x86_64-pc-linux-gnu
TIME,2026-10-20 03:11:02,1792470662
HEADER,CLIENT_LIST,Common Name,Real Address,Virtual Address,Virtual IPv6 Address,Bytes Received,Bytes Sent,Connected Since,Connected Since (time_t),Username,Client ID,Peer ID,Data Channel Cipher
CLIENT_LIST,ali,5.6.7.8:51820,10.1.0.2,,104857600,2097152000,2026-10-20 02:00:00,1792467600,ali,3,0,AES-256-GCM
CLIENT_LIST,ali,5.6.7.9:44100,10.1.0.3,,1024,2048,2026-10-20 03:00:00,1792471200,ali,7,1,AES-256-GCM
CLIENT_LIST,sara,9.9.9.9:1194,10.1.0.4,,500,900,2026-10-20 02:30:00,1792469400,sara,9,2,AES-256-GCM
HEADER,ROUTING_TABLE,Virtual Address,Common Name,Real Address,Last Ref,Last Ref (time_t)
ROUTING_TABLE,10.1.0.2,ali,5.6.7.8:51820,2026-10-20 03:10:00,1792470600
GLOBAL_STATS,Max bcast/mcast queue length,0
END
`

func TestTheStatusFileIsReadByColumnName(t *testing.T) {
	got := parseStatus(sampleStatus)
	if len(got) != 3 {
		t.Fatalf("expected three connected devices, got %d", len(got))
	}
	first := got[0]
	if first.Login != "ali" {
		t.Errorf("login read as %q", first.Login)
	}
	if first.BytesReceived != 104857600 || first.BytesSent != 2097152000 {
		t.Errorf("traffic read as %d/%d", first.BytesReceived, first.BytesSent)
	}
	if first.ClientID != "3" {
		t.Errorf("client id read as %q", first.ClientID)
	}
	if !first.ConnectedSince.Equal(time.Unix(1792467600, 0)) {
		t.Errorf("connected-since read as %v", first.ConnectedSince)
	}
}

// OpenVPN has added columns to this file between releases. A parser that
// counts positions starts attributing one customer's traffic to another the
// day the panel moves to a newer distribution, and nothing about that looks
// like a parsing bug.
func TestAnAddedColumnDoesNotShiftTheTraffic(t *testing.T) {
	future := `HEADER,CLIENT_LIST,Common Name,Real Address,Something New,Virtual Address,Virtual IPv6 Address,Bytes Received,Bytes Sent,Connected Since,Connected Since (time_t),Username,Client ID,Peer ID,Data Channel Cipher
CLIENT_LIST,ali,5.6.7.8:51820,whatever,10.1.0.2,,777,888,2026-10-20 02:00:00,1792467600,ali,3,0,AES-256-GCM
END
`
	got := parseStatus(future)
	if len(got) != 1 {
		t.Fatalf("expected one device, got %d", len(got))
	}
	if got[0].BytesReceived != 777 || got[0].BytesSent != 888 {
		t.Fatalf("a new column shifted the traffic: %d/%d", got[0].BytesReceived, got[0].BytesSent)
	}
}

// A status file that has not been written yet, or holds nothing but headers,
// means nobody is connected — not an error and not a crash.
func TestAnEmptyStatusIsNobodyConnected(t *testing.T) {
	for _, raw := range []string{"", "TITLE,OpenVPN\nEND\n", "garbage\n"} {
		if got := parseStatus(raw); len(got) != 0 {
			t.Errorf("%q produced %d sessions", raw, len(got))
		}
	}
}

// A row before any header cannot be read safely, so it is skipped rather than
// guessed at.
func TestARowWithNoHeaderIsSkipped(t *testing.T) {
	got := parseStatus("CLIENT_LIST,ali,5.6.7.8:1194,10.1.0.2,,1,2\nEND\n")
	if len(got) != 0 {
		t.Fatalf("a row was read with no header to read it by: %+v", got)
	}
}

// The device limit drops the newest connection, not the oldest. Dropping the
// oldest would let an account be handed around with each new person kicking
// the last one off, which is the thing a limit exists to prevent.
func TestTheDeviceLimitDropsTheNewestConnection(t *testing.T) {
	sessions := parseStatus(sampleStatus)
	over := OverLimit(sessions, func(login string) int {
		if login == "ali" {
			return 1
		}
		return 2
	})
	if len(over) != 1 {
		t.Fatalf("expected one device over the limit, got %d", len(over))
	}
	if over[0].ClientID != "7" {
		t.Fatalf("dropped client %s; the newest is 7", over[0].ClientID)
	}
}

// An account with no limit set is not policed at all, and one inside its
// limit is left alone.
func TestNobodyIsDroppedWithoutALimit(t *testing.T) {
	sessions := parseStatus(sampleStatus)
	if over := OverLimit(sessions, func(string) int { return 0 }); len(over) != 0 {
		t.Fatalf("devices were dropped with no limit set: %d", len(over))
	}
	if over := OverLimit(sessions, func(string) int { return 5 }); len(over) != 0 {
		t.Fatalf("devices were dropped while inside the limit: %d", len(over))
	}
}

// Disconnecting with nothing to disconnect must not reach for a socket.
//
// The reason is asserted, not just that it failed: with the guard taken out
// this still returns an error, because there is no socket to dial either.
// A test that passes for the wrong reason proves nothing about the guard.
func TestDisconnectRefusesAnEmptyClient(t *testing.T) {
	err := Disconnect(1, "  ")
	if err == nil {
		t.Fatal("an empty client id was accepted")
	}
	if !strings.Contains(err.Error(), "no client id") {
		t.Fatalf("it failed for some other reason than the missing id: %v", err)
	}
}
