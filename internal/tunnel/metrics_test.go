package tunnel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sampleMetrics is a snapshot as BackPack wrote it on the live panel for front 18.
const sampleMetrics = `{"name":"backpack-client-18","transport":"stealth","role":"client",` +
	`"taken":"2026-10-27T18:25:18.110755349Z","uptime":"3m30s","bytes_in":75264133,"bytes_out":699355553,` +
	`"peer":"185.231.182.144:3080","connected":true,"runtime":{"goroutines":57,"open_files":31},` +
	`"pool":{"live":11,"target":8,"configured":8,"mbps":0}}`

var sampleTaken = time.Date(2026, 10, 27, 18, 25, 18, 110755349, time.UTC)

func writeMetrics(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func metricsDir(t *testing.T) string {
	t.Helper()
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	return runtimeDirOverride
}

// BackPack names the snapshot after the config the panel started it with, one
// per leg; a wrong name or field reads as a front that carried nothing.
func TestTheSnapshotOfEachLegIsReadWhereBackPackWritesIt(t *testing.T) {
	dir := metricsDir(t)
	writeMetrics(t, dir, "backpack-client-18.metrics.json", sampleMetrics)
	writeMetrics(t, dir, "backpack-client-18-proxy.metrics.json",
		`{"name":"backpack-client-18-proxy","taken":"2026-10-27T18:25:01Z","bytes_in":4096,"bytes_out":81920}`)

	got := FrontMetrics(18, sampleTaken.Add(10*time.Second))
	want := map[string]LegMetrics{
		"18":       {Taken: sampleTaken, BytesIn: 75264133, BytesOut: 699355553},
		"18-proxy": {Taken: time.Date(2026, 10, 27, 18, 25, 1, 0, time.UTC), BytesIn: 4096, BytesOut: 81920},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d legs %v, want %v", len(got), got, want)
	}
	for key, w := range want {
		g, ok := got[key]
		if !ok || !g.Taken.Equal(w.Taken) || g.BytesIn != w.BytesIn || g.BytesOut != w.BytesOut {
			t.Errorf("leg %s read as %+v (present %v), want %+v", key, g, ok, w)
		}
	}
}

// A snapshot BackPack stopped rewriting is a client that is gone; its counters
// are not the front's traffic now, and a missing or unreadable one is nothing.
func TestOnlyAFreshReadableSnapshotCounts(t *testing.T) {
	leg := Pair{Id: 18, Tool: ToolBackPack}
	for _, tc := range []struct {
		name string
		body string // "" writes no file
		age  time.Duration
		ok   bool
	}{
		{"written a moment ago", sampleMetrics, 10 * time.Second, true},
		{"just inside three minutes", sampleMetrics, 3*time.Minute - time.Second, true},
		{"older than three minutes", sampleMetrics, 3*time.Minute + time.Second, false},
		{"missing", "", 0, false},
		// json fills what it can around a bad field: a counter read as 0 is a restart.
		{"a counter in a shape the panel does not know", `{"taken":"2026-10-27T18:25:18.110755349Z","bytes_in":"75264133","bytes_out":699355553}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := metricsDir(t)
			if tc.body != "" {
				writeMetrics(t, dir, "backpack-client-18.metrics.json", tc.body)
			}
			m, ok := readLegMetrics(leg, sampleTaken.Add(tc.age))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (read %+v)", ok, tc.ok, m)
			}
			if !ok && m != (LegMetrics{}) {
				t.Errorf("a refused snapshot still handed back %+v", m)
			}
		})
	}
}

// A deleted front's snapshot stays fresh for minutes, so a new front given its
// id would count the old one's totals as its own first reading.
func TestRemovingAFrontsSnapshotsLeavesOtherFrontsAlone(t *testing.T) {
	dir := metricsDir(t)
	for _, f := range []string{"backpack-client-18.metrics.json", "backpack-client-18-proxy.metrics.json", "backpack-client-180.metrics.json"} {
		writeMetrics(t, dir, f, sampleMetrics)
	}
	RemoveFrontMetrics(18)
	for f, kept := range map[string]bool{
		"backpack-client-18.metrics.json":       false,
		"backpack-client-18-proxy.metrics.json": false,
		"backpack-client-180.metrics.json":      true,
	} {
		_, err := os.Stat(filepath.Join(dir, f))
		if exists := err == nil; exists != kept {
			t.Errorf("%s exists = %v, want %v", f, exists, kept)
		}
	}
}

// A reading counts only what moved since the one before; a client that began
// again from zero has carried all it now shows, never a negative amount.
func TestALegCountsWhatMovedSinceTheLastReading(t *testing.T) {
	last := LegMetrics{BytesIn: 1000, BytesOut: 5000}
	for _, tc := range []struct {
		name            string
		cur, prev       LegMetrics
		wantIn, wantOut int64
	}{
		{"grew", LegMetrics{BytesIn: 1600, BytesOut: 8000}, last, 600, 3000},
		{"began again, uploads already past", LegMetrics{BytesIn: 1200, BytesOut: 300}, last, 1200, 300},
		{"began again, downloads already past", LegMetrics{BytesIn: 200, BytesOut: 6000}, last, 200, 6000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, out := tc.cur.Since(tc.prev)
			if in != tc.wantIn || out != tc.wantOut {
				t.Errorf("Since = %d in, %d out; want %d, %d", in, out, tc.wantIn, tc.wantOut)
			}
		})
	}
}
