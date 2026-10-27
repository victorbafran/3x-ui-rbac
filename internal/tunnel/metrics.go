package tunnel

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// metricsMaxAge is how old a leg's counters may be and still be its client's:
// BackPack rewrites them every 30 seconds while the client runs.
const metricsMaxAge = 3 * time.Minute

// backpackLegs are the legs a BackPack front can run, as Legs names them.
var backpackLegs = []string{"", LegProxy}

// LegMetrics is the part of BackPack's per-leg snapshot the panel reads, and
// what the panel keeps of it between readings.
type LegMetrics struct {
	Taken    time.Time `json:"taken"`
	BytesIn  int64     `json:"bytes_in"`
	BytesOut int64     `json:"bytes_out"`
}

// metricsPath is where BackPack writes a leg's snapshot: beside the config it
// was started with, named after that file.
func (p Pair) metricsPath() string {
	return strings.TrimSuffix(p.localConfigPath(), ".toml") + ".metrics.json"
}

// readLegMetrics reads one BackPack leg's counters as of now; ok is false for a
// snapshot missing, unreadable or stale.
func readLegMetrics(p Pair, now time.Time) (m LegMetrics, ok bool) {
	raw, err := os.ReadFile(p.metricsPath())
	if err != nil {
		return LegMetrics{}, false
	}
	if json.Unmarshal(raw, &m) != nil || now.Sub(m.Taken) > metricsMaxAge {
		return LegMetrics{}, false
	}
	return m, true
}

// FrontMetrics reads every leg BackPack front id can run, by leg key. A leg the
// front no longer runs drops out once its snapshot goes stale.
func FrontMetrics(id int, now time.Time) map[string]LegMetrics {
	out := map[string]LegMetrics{}
	for _, leg := range backpackLegs {
		p := Pair{Id: id, Tool: ToolBackPack, Leg: leg}
		if m, ok := readLegMetrics(p, now); ok {
			out[p.Key()] = m
		}
	}
	return out
}

// RemoveFrontMetrics deletes a front's snapshots. One outlives its client by up
// to metricsMaxAge, and a later front given the same id would count it as its own.
func RemoveFrontMetrics(id int) {
	for _, leg := range backpackLegs {
		_ = os.Remove(Pair{Id: id, Tool: ToolBackPack, Leg: leg}.metricsPath())
	}
}

// Since is what the leg carried after prev, an earlier reading, or all of it
// with none. A counter below prev's means the client began again from zero.
func (m LegMetrics) Since(prev LegMetrics) (in, out int64) {
	if m.BytesIn < prev.BytesIn || m.BytesOut < prev.BytesOut {
		return m.BytesIn, m.BytesOut
	}
	return m.BytesIn - prev.BytesIn, m.BytesOut - prev.BytesOut
}
