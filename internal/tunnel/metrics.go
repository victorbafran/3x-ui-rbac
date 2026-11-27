package tunnel

import (
	"encoding/json"
	"net"
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

// connectGrace is how long a freshly started client is given to raise its control
// channel before "not connected" counts against the front.
const connectGrace = 90 * time.Second

// FrontConnected is what BackPack front id's main client says of its own control
// channel, from its latest snapshot. A front's tunnel port can take a TCP dial while
// everything the tunnel sends is dropped on the way, as a protocol filter does to a
// stealth tunnel (seen 2026-11-27): the dial alone then calls a dead front healthy.
// known is false with no fresh snapshot, one naming another address (a swap has just
// moved the front), one from a client up for less than connectGrace, or one from a
// binary that does not say; the dial alone decides then. A client holding no channel
// names no peer at all (seen live), so a missing peer is taken as this address: a swap
// restarts the client, and connectGrace covers the restart. One that has not connected
// once since it started says nothing either (seen live, the filtered front after a
// restart); a snapshot that carries the runtime block, which the binary the panel pins
// writes alongside connected, and no connected is taken as never connected.
func FrontConnected(id int, address string, now time.Time) (known, up bool) {
	raw, err := os.ReadFile(Pair{Id: id, Tool: ToolBackPack}.metricsPath())
	if err != nil {
		return false, false
	}
	var snap struct {
		Taken     time.Time       `json:"taken"`
		Uptime    string          `json:"uptime"`
		Peer      string          `json:"peer"`
		Connected *bool           `json:"connected"`
		Runtime   json.RawMessage `json:"runtime"`
	}
	if json.Unmarshal(raw, &snap) != nil || now.Sub(snap.Taken) > metricsMaxAge {
		return false, false
	}
	if snap.Connected == nil {
		if len(snap.Runtime) == 0 {
			return false, false
		}
		never := false
		snap.Connected = &never
	}
	if snap.Peer != "" {
		if host, _, err := net.SplitHostPort(snap.Peer); err != nil || host != strings.TrimSpace(address) {
			return false, false
		}
	}
	if up, err := time.ParseDuration(snap.Uptime); err != nil || up < connectGrace {
		return false, false
	}
	return true, *snap.Connected
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
