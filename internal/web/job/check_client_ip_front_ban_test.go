package job

import (
	"slices"
	"testing"
	"time"
)

// No test reaches an entry server over SSH; one that wants the bans records them.
func init() { banOnFronts = func([]string) {} }

func recordFrontBans(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	saved := banOnFronts
	banOnFronts = func(ips []string) { calls = append(calls, ips) }
	t.Cleanup(func() { banOnFronts = saved })
	return &calls
}

// The address banned here is banned at the entry servers too, where a customer
// behind one connects; fail2ban on this panel only ever sees the tunnel.
func TestABanReachesTheEntryServers(t *testing.T) {
	setupIntegrationDB(t)
	calls := recordFrontBans(t)

	const email = "front-ban@x"
	seedLinkedInboundWithClient(t, "front-ban", email, 1)
	now := time.Now().Unix()
	seedClientIps(t, email, []IPWithTimestamp{{IP: "203.0.113.60", Timestamp: now - 500}})

	j := NewCheckClientIpJob()
	j.processObserved(map[string]map[string]int64{
		email: {"203.0.113.60": now - 400, "203.0.113.61": now},
	}, true, true)
	if len(*calls) != 1 || !slices.Equal((*calls)[0], []string{"203.0.113.60"}) {
		t.Fatalf("entry servers were handed %v, want the one old address", *calls)
	}

	// The same frozen address is not banned again, here or there.
	j.processObserved(map[string]map[string]int64{
		email: {"203.0.113.60": now - 400, "203.0.113.61": now},
	}, true, true)
	if len(*calls) != 1 {
		t.Errorf("a repeat scan banned again at the entry servers: %v", *calls)
	}
}

// A client within its limit bans nobody anywhere.
func TestNoBanReachesTheEntryServersWithinTheLimit(t *testing.T) {
	setupIntegrationDB(t)
	calls := recordFrontBans(t)

	const email = "front-ok@x"
	seedLinkedInboundWithClient(t, "front-ok", email, 2)
	now := time.Now().Unix()
	seedClientIps(t, email, []IPWithTimestamp{{IP: "203.0.113.70", Timestamp: now - 500}})

	NewCheckClientIpJob().processObserved(map[string]map[string]int64{
		email: {"203.0.113.70": now - 400, "203.0.113.71": now},
	}, true, true)
	if len(*calls) != 0 {
		t.Errorf("a client within its limit was banned at the entry servers: %v", *calls)
	}
}

func TestEachBannedAddressGoesOnce(t *testing.T) {
	got := bannedIPs([]pendingBan{
		{banned: []IPWithTimestamp{{IP: "203.0.113.1"}, {IP: "203.0.113.2"}}},
		{banned: []IPWithTimestamp{{IP: "203.0.113.2"}, {IP: "203.0.113.3"}}},
		{},
	})
	if want := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}; !slices.Equal(got, want) {
		t.Errorf("addresses %v, want %v", got, want)
	}
}
