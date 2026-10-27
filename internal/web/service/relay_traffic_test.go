package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// seedBackPackFront makes an enabled BackPack front with a fresh speed table.
func seedBackPackFront(t *testing.T) model.Relay {
	t.Helper()
	setupRelayDB(t)
	reset := func() {
		frontRates.Lock()
		frontRates.byRelay = map[int]map[string]legRate{}
		frontRates.Unlock()
	}
	reset()
	t.Cleanup(reset)
	r := model.Relay{Name: "iran", Address: "203.0.113.1", KeyPath: "/k", Tunnel: "backpack", Role: model.RelayFront, Enabled: true}
	if err := database.GetDB().Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

// writeLegSnapshot leaves a snapshot where BackPack writes one for the leg key.
func writeLegSnapshot(t *testing.T, legKey string, taken time.Time, in, out int64) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XUI_DB_FOLDER"), "tunnel")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"backpack-client-%s","transport":"stealth","role":"client","taken":%q,"uptime":"3m30s","bytes_in":%d,"bytes_out":%d}`,
		legKey, taken.UTC().Format(time.RFC3339Nano), in, out)
	if err := os.WriteFile(filepath.Join(dir, "backpack-client-"+legKey+".metrics.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func frontRow(t *testing.T, id int) model.Relay {
	t.Helper()
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func wantTotals(t *testing.T, id int, in, out int64) {
	t.Helper()
	if r := frontRow(t, id); r.TrafficIn != in || r.TrafficOut != out {
		t.Fatalf("totals are %d in, %d out; want %d, %d", r.TrafficIn, r.TrafficOut, in, out)
	}
}

// Every reading adds only what moved since the one before, on each leg.
func TestFrontTrafficCountsEachReadingOnce(t *testing.T) {
	r := seedBackPackFront(t)
	main, proxy := strconv.Itoa(r.Id), strconv.Itoa(r.Id)+"-proxy"
	base := time.Now().Add(-90 * time.Second)
	s := &RelayService{}
	for _, step := range []struct {
		name            string
		write           func()
		wantIn, wantOut int64
	}{
		{"the first reading counts in full", func() { writeLegSnapshot(t, main, base, 1000, 5000) }, 1000, 5000},
		{"the next reading adds the difference", func() { writeLegSnapshot(t, main, base.Add(30*time.Second), 1600, 8000) }, 1600, 8000},
		{"the real-IP leg adds its own", func() { writeLegSnapshot(t, proxy, base.Add(31*time.Second), 100, 400) }, 1700, 8400},
		{"a client that began again adds all it shows", func() { writeLegSnapshot(t, main, base.Add(60*time.Second), 200, 300) }, 1900, 8700},
	} {
		step.write()
		s.CollectFrontTraffic()
		if row := frontRow(t, r.Id); row.TrafficIn != step.wantIn || row.TrafficOut != step.wantOut {
			t.Fatalf("%s: totals are %d in, %d out; want %d, %d", step.name, row.TrafficIn, row.TrafficOut, step.wantIn, step.wantOut)
		}
	}
}

// The client outlives a panel-only restart and its counters with it, so the
// stored reading, not nothing, is what the first one after a restart is against.
func TestFrontTrafficCarriesOnFromTheStoredReadingAfterARestart(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	before := time.Now().Add(-time.Minute)
	stored := fmt.Sprintf(`{%q:{"taken":%q,"bytes_in":1000,"bytes_out":5000}}`, key, before.UTC().Format(time.RFC3339Nano))
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).Updates(map[string]any{
		"traffic_in": 1000, "traffic_out": 5000, "metrics_last": stored,
	}).Error; err != nil {
		t.Fatal(err)
	}
	writeLegSnapshot(t, key, before.Add(30*time.Second), 1500, 7000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1500, 7000)
}

// A collector holding a row read before another one's write must not add the
// same delta again on top of it.
func TestAReadingIsNotAddedTwiceFromAStaleRow(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	read := map[string]tunnel.LegMetrics{key: {Taken: time.Now(), BytesIn: 1000, BytesOut: 5000}}
	stale := frontRow(t, r.Id)
	collectFrontTraffic(stale, read)
	collectFrontTraffic(stale, read)
	wantTotals(t, r.Id, 1000, 5000)
}

// A stored reading that cannot be read is not a reason to count every total a
// second time: counting carries on from the next reading.
func TestAnUnreadableStoredReadingCountsOnFromTheNextOne(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).Updates(map[string]any{
		"traffic_in": 1000, "traffic_out": 5000, "metrics_last": `{"broken`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Minute)
	writeLegSnapshot(t, key, base, 1500, 7000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1000, 5000)
	writeLegSnapshot(t, key, base.Add(30*time.Second), 1600, 7100)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 1100, 5100)
}

// The page shows a front's speed over its last interval, summed across legs, kept
// while BackPack has not rewritten its snapshot and gone once two minutes old.
func TestAFrontsSpeedIsItsLastIntervalAndFades(t *testing.T) {
	r := seedBackPackFront(t)
	main, proxy := strconv.Itoa(r.Id), strconv.Itoa(r.Id)+"-proxy"
	first := time.Now().Add(-40 * time.Second)
	last := first.Add(30 * time.Second)
	s := &RelayService{}
	writeLegSnapshot(t, main, first, 0, 0)
	writeLegSnapshot(t, proxy, first, 0, 0)
	s.CollectFrontTraffic()
	writeLegSnapshot(t, main, last, 3000, 30000)
	writeLegSnapshot(t, proxy, last, 600, 1200)
	s.CollectFrontTraffic()
	s.CollectFrontTraffic()

	views, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].RateIn != 120 || views[0].RateOut != 1040 {
		t.Fatalf("the page reads %+v; want 120 B/s in and 1040 B/s out", views)
	}
	if in, out := frontSpeed(r.Id, last.Add(frontRateWindow-time.Second)); in != 120 || out != 1040 {
		t.Errorf("just inside two minutes the speed is %v in, %v out; want 120, 1040", in, out)
	}
	if in, out := frontSpeed(r.Id, last.Add(frontRateWindow+time.Second)); in != 0 || out != 0 {
		t.Errorf("a speed over two minutes old still reads %v in, %v out", in, out)
	}
}

// BackPack rewrites an idle front's snapshot every 30s; nothing moved is no
// write, a speed of 0, and the next speed is over the time since that reading.
func TestAnIdleFrontIsNotRewrittenAndItsSpeedFallsToZero(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	base := time.Now().Add(-100 * time.Second)
	s := &RelayService{}
	writeLegSnapshot(t, key, base, 0, 0)
	s.CollectFrontTraffic()
	writeLegSnapshot(t, key, base.Add(30*time.Second), 3000, 30000)
	s.CollectFrontTraffic()
	stored := frontRow(t, r.Id).MetricsLast

	writeLegSnapshot(t, key, base.Add(60*time.Second), 3000, 30000)
	s.CollectFrontTraffic()
	if got := frontRow(t, r.Id).MetricsLast; got != stored {
		t.Fatalf("an idle reading was written: %s, was %s", got, stored)
	}
	if in, out := frontSpeed(r.Id, time.Now()); in != 0 || out != 0 {
		t.Fatalf("an idle front still reads %v in, %v out", in, out)
	}

	writeLegSnapshot(t, key, base.Add(90*time.Second), 3600, 36000)
	s.CollectFrontTraffic()
	wantTotals(t, r.Id, 3600, 36000)
	if in, out := frontSpeed(r.Id, time.Now()); in != 20 || out != 200 {
		t.Errorf("after the idle reading the speed is %v in, %v out; want 20, 200", in, out)
	}
}

// Traffic across a gap longer than the speed window still counts, but an
// average over it is not what is moving now.
func TestAReadingAfterALongGapIsNoSpeed(t *testing.T) {
	r := seedBackPackFront(t)
	key := strconv.Itoa(r.Id)
	taken := time.Now().Add(-10 * time.Second)
	stored := fmt.Sprintf(`{%q:{"taken":%q,"bytes_in":0,"bytes_out":0}}`, key, taken.Add(-5*time.Minute).UTC().Format(time.RFC3339Nano))
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).Update("metrics_last", stored).Error; err != nil {
		t.Fatal(err)
	}
	writeLegSnapshot(t, key, taken, 3000, 30000)
	(&RelayService{}).CollectFrontTraffic()
	wantTotals(t, r.Id, 3000, 30000)
	if in, out := frontSpeed(r.Id, time.Now()); in != 0 || out != 0 {
		t.Errorf("five minutes' traffic read as a speed of %v in, %v out", in, out)
	}
}

// A standby copy dials no front; its readings are not the main panel's to count.
func TestAStandbyCopyCountsNoFrontTraffic(t *testing.T) {
	r := seedBackPackFront(t)
	writeLegSnapshot(t, strconv.Itoa(r.Id), time.Now(), 1000, 5000)
	global.SetStandby(true)
	t.Cleanup(func() { global.SetStandby(false) })
	(&RelayService{}).CollectFrontTraffic()
	if row := frontRow(t, r.Id); row.TrafficIn != 0 || row.TrafficOut != 0 || row.MetricsLast != "" {
		t.Fatalf("a standby copy counted %d in, %d out, stored %q", row.TrafficIn, row.TrafficOut, row.MetricsLast)
	}
}
