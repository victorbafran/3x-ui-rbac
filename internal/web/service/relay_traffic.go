package service

import (
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"

	"gorm.io/gorm"
)

// frontRateWindow is how recent a front's speed must be to be shown, and the
// longest gap between two readings still worth calling a speed.
const frontRateWindow = 2 * time.Minute

// legRate is one leg's speed in bytes a second between its last two readings.
type legRate struct {
	in, out float64
	at      time.Time
}

// frontRates holds each front's leg speeds, by relay id then leg key. Memory
// only: a speed from before the panel started is not a speed now.
var frontRates = struct {
	sync.Mutex
	byRelay map[int]map[string]legRate
}{byRelay: map[int]map[string]legRate{}}

// CollectFrontTraffic adds to each enabled front what its tunnel carried since the
// last reading: a BackPack front's from its client's counters, a Backhaul one's from the firewall's.
func (s *RelayService) CollectFrontTraffic() {
	if global.IsStandby() {
		return
	}
	var fronts []model.Relay
	if err := database.GetDB().Where("enabled = ? AND role = ?", true, model.RelayFront).
		Find(&fronts).Error; err != nil {
		logger.Warning("front traffic: could not list the entry servers:", err)
		return
	}
	now := time.Now()
	var counted map[int]map[string]tunnel.LegMetrics
	if slices.ContainsFunc(fronts, func(r model.Relay) bool { return frontTool(r) == tunnel.ToolBackhaul }) {
		var err error
		counted, err = readFrontAcct(frontAcctPairs(fronts), now)
		frontAcctReadErr.note("front traffic: could not read the Backhaul fronts' counters:", err)
	}
	for _, r := range fronts {
		read := counted[r.Id]
		if frontTool(r) == tunnel.ToolBackPack {
			read = tunnel.FrontMetrics(r.Id, now)
		}
		collectFrontTraffic(r, read)
	}
}

// Variables so a test stands in for the panel's own netfilter.
var (
	fitFrontAcct  = tunnel.FitFrontAcct
	readFrontAcct = tunnel.ReadFrontAcct
)

// countBackhaulFronts has the firewall count every enabled Backhaul front's tunnel and
// no other; a standby copy dials none, so it counts none.
func (s *RelayService) countBackhaulFronts() {
	var fronts []model.Relay
	if !global.IsStandby() {
		if err := database.GetDB().Where("enabled = ? AND role = ?", true, model.RelayFront).
			Find(&fronts).Error; err != nil {
			logger.Warning("front traffic: could not list the entry servers:", err)
			return
		}
	}
	frontAcctFitErr.note("front traffic: the Backhaul fronts cannot be counted:", fitFrontAcct(frontAcctPairs(fronts)))
}

// frontAcctPairs is what the firewall counts each front by: the address and port its rows name now.
func frontAcctPairs(fronts []model.Relay) []tunnel.Pair {
	pairs := make([]tunnel.Pair, 0, len(fronts))
	for _, r := range fronts {
		pairs = append(pairs, tunnel.Pair{
			Id: r.Id, FrontAddress: r.Address, TunnelPort: r.TunnelPort,
			Transport: tunnel.Transport(r.Transport), Tool: frontTool(r),
		})
	}
	return pairs
}

// onceEach logs an error when it differs from the last one: a box whose firewall
// refuses would otherwise say so twice a minute for ever.
type onceEach struct {
	sync.Mutex
	last string
}

var frontAcctFitErr, frontAcctReadErr onceEach

func (o *onceEach) note(what string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	o.Lock()
	same := msg == o.last
	o.last = msg
	o.Unlock()
	if err != nil && !same {
		logger.Warning(what, err)
	}
}

// collectFrontTraffic folds one front's readings into its row. The write lands
// only on the reading it started from, so no delta is ever added twice.
func collectFrontTraffic(r model.Relay, read map[string]tunnel.LegMetrics) {
	last := map[string]tunnel.LegMetrics{}
	rebase := false
	if r.MetricsLast != "" && json.Unmarshal([]byte(r.MetricsLast), &last) != nil {
		// Counting from nothing would add every total a second time.
		logger.Warningf("front %s: its last traffic reading is unreadable; counting on from this one", r.Name)
		last, rebase = map[string]tunnel.LegMetrics{}, true
	}

	next := make(map[string]tunnel.LegMetrics, len(last)+len(read))
	maps.Copy(next, last)
	idle := frontIdleMarks(r.Id)
	rates := map[string]legRate{}
	var addIn, addOut int64
	advanced, moved := false, rebase
	for key, cur := range read {
		prev, had := last[key]
		// Not rewritten since: nothing moved, and a zero gap makes the speed 0/0.
		if had && !cur.Taken.After(prev.Taken) {
			continue
		}
		next[key], advanced = cur, true
		if rebase {
			continue
		}
		if had && cur.BytesIn == prev.BytesIn && cur.BytesOut == prev.BytesOut {
			rates[key] = legRate{at: cur.Taken}
			continue
		}
		moved = true
		in, out := cur.Since(prev)
		addIn += in
		addOut += out
		// A leg read idle after prev moved only since that reading.
		from := prev.Taken
		if at, ok := idle[key]; ok && at.After(from) {
			from = at
		}
		if gap := cur.Taken.Sub(from); had && gap > 0 && gap <= frontRateWindow {
			rates[key] = legRate{in: float64(in) / gap.Seconds(), out: float64(out) / gap.Seconds(), at: cur.Taken}
		}
	}
	if !advanced {
		return
	}
	// An idle front costs no write each tick; only its speed drops to 0.
	if !moved {
		noteFrontRates(r.Id, rates)
		return
	}

	encoded, err := json.Marshal(next)
	if err != nil {
		logger.Warningf("front %s: could not record its traffic: %v", r.Name, err)
		return
	}
	res := database.GetDB().Model(&model.Relay{}).
		Where("id = ? AND metrics_last = ?", r.Id, r.MetricsLast).
		Updates(map[string]any{
			"traffic_in":   gorm.Expr("traffic_in + ?", addIn),
			"traffic_out":  gorm.Expr("traffic_out + ?", addOut),
			"metrics_last": string(encoded),
		})
	if res.Error != nil {
		logger.Warningf("front %s: could not record its traffic: %v", r.Name, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	noteFrontRates(r.Id, rates)
}

func noteFrontRates(id int, rates map[string]legRate) {
	frontRates.Lock()
	defer frontRates.Unlock()
	legs := frontRates.byRelay[id]
	if legs == nil {
		legs = map[string]legRate{}
		frontRates.byRelay[id] = legs
	}
	maps.Copy(legs, rates)
}

// frontIdleMarks is when each leg of front id was last read with nothing moved
// since its stored reading, which an idle front no longer rewrites.
func frontIdleMarks(id int) map[string]time.Time {
	frontRates.Lock()
	defer frontRates.Unlock()
	out := map[string]time.Time{}
	for key, leg := range frontRates.byRelay[id] {
		if leg.in == 0 && leg.out == 0 {
			out[key] = leg.at
		}
	}
	return out
}

// frontSpeed is a front's speed at now in bytes a second: the sum of its legs'
// last readings, each left out once older than frontRateWindow.
func frontSpeed(id int, now time.Time) (in, out float64) {
	frontRates.Lock()
	defer frontRates.Unlock()
	for _, leg := range frontRates.byRelay[id] {
		if now.Sub(leg.at) <= frontRateWindow {
			in += leg.in
			out += leg.out
		}
	}
	return in, out
}
