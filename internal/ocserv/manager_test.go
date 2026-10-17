package ocserv

import "testing"

// A client edit must not restart ocserv. ocserv reads its password file on
// every authentication, so adding or removing a client takes effect on the
// next login by itself — restarting instead would drop every other customer
// who happens to be online at the time.
func TestEnsureActionForKeepsLiveSessionsThroughAClientEdit(t *testing.T) {
	cases := []struct {
		name                                     string
		running                                  bool
		curStruct, curUsers, newStruct, newUsers string
		want                                     ensureAction
	}{
		{"nothing changed", true, "s1", "u1", "s1", "u1", ensureNoop},
		{"a client was added", true, "s1", "u1", "s1", "u1|u2", ensureUsers},
		{"a client was removed", true, "s1", "u1|u2", "s1", "u1", ensureUsers},
		{"the port moved", true, "s1", "u1", "s2", "u1", ensureRestart},
		{"port and clients both moved", true, "s1", "u1", "s2", "u2", ensureRestart},
		{"the process died", false, "s1", "u1", "s1", "u1", ensureRestart},
		{"never started", false, "", "", "s1", "u1", ensureRestart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureActionFor(tc.running, tc.curStruct, tc.curUsers, tc.newStruct, tc.newUsers)
			if got != tc.want {
				t.Fatalf("ensureActionFor = %d, want %d", got, tc.want)
			}
		})
	}
}

// occtl reports a counter that is cumulative for one session and restarts at
// zero when the client reconnects. Folding by session id is what keeps a
// reconnect from reading as a negative delta.
func TestFoldSessionsCountsGrowthOfAKnownSession(t *testing.T) {
	last := map[int64]sessionCounters{7: {rx: 1000, tx: 2000}}
	deltas, newLast := foldSessions([]Session{{ID: 7, Username: "ann", RX: 1500, TX: 2200}}, last)

	if got := deltas["ann"]; got.rx != 500 || got.tx != 200 {
		t.Fatalf("delta = rx %d tx %d, want the growth 500/200", got.rx, got.tx)
	}
	if got := newLast[7]; got.rx != 1500 || got.tx != 2200 {
		t.Fatalf("newLast = %+v, want the absolute totals", got)
	}
}

func TestFoldSessionsCountsAFirstSightInFull(t *testing.T) {
	deltas, _ := foldSessions([]Session{{ID: 9, Username: "bob", RX: 300, TX: 700}}, map[int64]sessionCounters{})
	if got := deltas["bob"]; got.rx != 300 || got.tx != 700 {
		t.Fatalf("delta = rx %d tx %d, want the whole total 300/700", got.rx, got.tx)
	}
}

// The trap this arithmetic exists for: the same customer reconnects, so their
// new session starts from zero. Keying the previous totals by user instead of
// by session would compute 50-5000 and bill nothing at all, forever.
func TestFoldSessionsDoesNotLoseTrafficWhenAClientReconnects(t *testing.T) {
	last := map[int64]sessionCounters{100: {rx: 5000, tx: 5000}}
	sessions := []Session{{ID: 101, Username: "ann", RX: 50, TX: 60}}

	deltas, newLast := foldSessions(sessions, last)

	if got := deltas["ann"]; got.rx != 50 || got.tx != 60 {
		t.Fatalf("after a reconnect delta = rx %d tx %d, want the new session's 50/60", got.rx, got.tx)
	}
	if _, stale := newLast[100]; stale {
		t.Fatal("the ended session is still being carried forward")
	}
}

// One customer on two devices has two sessions at once. Their traffic is one
// bill, so the deltas add up rather than the last device winning.
func TestFoldSessionsSumsTwoDevicesOfOneClient(t *testing.T) {
	last := map[int64]sessionCounters{1: {rx: 10, tx: 10}, 2: {rx: 100, tx: 100}}
	sessions := []Session{
		{ID: 1, Username: "ann", RX: 60, TX: 20},
		{ID: 2, Username: "ann", RX: 150, TX: 400},
	}
	deltas, _ := foldSessions(sessions, last)
	if got := deltas["ann"]; got.rx != 100 || got.tx != 310 {
		t.Fatalf("two devices folded to rx %d tx %d, want 100/310", got.rx, got.tx)
	}
}

// ocserv reuses session ids across restarts. A counter that went backwards is
// therefore a new session wearing an old id, and counting its total again
// would bill the customer for traffic they already paid for.
func TestFoldSessionsIgnoresACounterThatWentBackwards(t *testing.T) {
	last := map[int64]sessionCounters{5: {rx: 9000, tx: 9000}}
	deltas, _ := foldSessions([]Session{{ID: 5, Username: "ann", RX: 10, TX: 10}}, last)
	if got, ok := deltas["ann"]; ok {
		t.Fatalf("a backwards counter produced a delta of %+v, want none", got)
	}
}

func TestFoldSessionsSkipsIdleSessions(t *testing.T) {
	last := map[int64]sessionCounters{3: {rx: 42, tx: 42}}
	deltas, newLast := foldSessions([]Session{{ID: 3, Username: "ann", RX: 42, TX: 42}}, last)
	if len(deltas) != 0 {
		t.Fatalf("an unchanged session produced %v", deltas)
	}
	if got := newLast[3]; got.rx != 42 {
		t.Fatal("an idle session was dropped from the carried-forward totals")
	}
}
