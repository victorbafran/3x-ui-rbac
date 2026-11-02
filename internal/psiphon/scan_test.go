package psiphon

import (
	"context"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRegions runs the "regions" stand-in, connecting for up only, with short
// scan budgets; it returns the file each start logs to.
func fakeRegions(t *testing.T, up string, connectBudget time.Duration) string {
	t.Helper()
	useFakeClient(t, "regions")
	useFakeExit(t)
	t.Setenv(fakeUpEnv, up)
	log := filepath.Join(t.TempDir(), "starts.log")
	t.Setenv(fakeLogEnv, log)
	savedBudget, savedTick := scanConnectBudget, scanTick
	scanConnectBudget, scanTick = connectBudget, 20*time.Millisecond
	t.Cleanup(func() { scanConnectBudget, scanTick = savedBudget, savedTick })
	return log
}

func scanDirs() []string {
	entries, _ := os.ReadDir(filepath.Join(Root(), "_scan"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func scanStarts(t *testing.T, log string) []fakeStart {
	t.Helper()
	var out []fakeStart
	for _, s := range fakeStarts(t, log) {
		if strings.Contains(s.dir, string(filepath.Separator)+"_scan"+string(filepath.Separator)) {
			out = append(out, s)
		}
	}
	return out
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// leftNothingBehind fails if a scan client's process, directory or the scan root survived.
func leftNothingBehind(t *testing.T, starts []fakeStart) {
	t.Helper()
	for _, s := range starts {
		if listening(s.httpPort) {
			t.Errorf("%s's scan client still holds port %d", s.region, s.httpPort)
		}
		if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
			t.Errorf("%s's scan directory %s is still there: %v", s.region, s.dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(Root(), "_scan")); !os.IsNotExist(err) {
		t.Errorf("the scan root is still there: %v", err)
	}
}

func waitScanDone(t *testing.T, m *Manager) ScanState {
	t.Helper()
	waitFor(t, "the scan to finish", func() bool { return !m.ScanState().Running })
	return m.ScanState()
}

// Measured 2026-10-31: Finland connected though no list carried it, Italy did not.
// A scan tries each country with a client of its own and says which work and how well.
// Czechia ran and tested fine, yet its scan, a second client beside the first, never
// connected: a running country is measured through its own client and gets no other.
func TestAScanMeasuresEachCountryWithoutTouchingTheRunningOnes(t *testing.T) {
	log := fakeRegions(t, "FI,DE", 1500*time.Millisecond)
	m := newManager()
	t.Cleanup(m.StopAll)
	de := Country{"DE", 11181, 11381}
	if err := m.Reconcile([]Country{de}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	customersDE := m.procOf("DE")

	// The panel's own ports and one some other program listens on are skipped.
	taken := func(port int) bool { return port >= scanPortFirst && port < scanPortFirst+5 }
	busy, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(scanPortFirst+5)))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	// US is enabled but its client is not running, so it gets a client of its own.
	plan := ScanPlan{
		Codes:   []string{"IT", "FI", "DE", "HU", "FI", "US"},
		Running: []Country{de, {"US", 11182, 11382}},
		Known:   []string{"DE", "IT", "HU", "US"},
		Taken:   taken,
	}
	if err := m.StartScan(plan); err != nil {
		t.Fatalf("start the scan: %v", err)
	}
	if err := m.StartScan(ScanPlan{Codes: []string{"US"}, Taken: taken}); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("a second scan while one runs: %v, want ErrScanRunning", err)
	}
	st := waitScanDone(t, m)

	if st.Total != 5 || st.Stopped || len(st.Results) != 5 {
		t.Fatalf("scan state %+v; want 5 countries, all finished", st)
	}
	byCode := map[string]ScanResult{}
	for _, r := range st.Results {
		byCode[r.Code] = r
	}
	fastest := int64(float64(fakeDownBytes) / fakeDownPause.Seconds())
	for _, code := range []string{"FI", "DE"} {
		r := byCode[code]
		if !r.Connected || !r.Exit.Works() || r.Exit.IP != fakeExitIP || r.Exit.Loc != code {
			t.Errorf("%s: %+v; want a working exit from %s at %s", code, r, code, fakeExitIP)
		}
		if r.Exit.Latency < fakeTraceDelay || r.Exit.Latency > 10*time.Second {
			t.Errorf("%s latency %v; the trace took at least %v", code, r.Exit.Latency, fakeTraceDelay)
		}
		if r.Exit.BytesPerSec <= 0 || r.Exit.BytesPerSec > fastest {
			t.Errorf("%s speed %d B/s; the download paused %v over %d bytes, so at most %d", code, r.Exit.BytesPerSec, fakeDownPause, fakeDownBytes, fastest)
		}
	}
	for _, code := range []string{"IT", "HU", "US"} {
		if r := byCode[code]; r.Connected || r.Exit.IP != "" || r.Exit.Works() || r.OnRunning {
			t.Errorf("%s: %+v; it never connected", code, r)
		}
	}
	if !byCode["DE"].OnRunning || byCode["FI"].OnRunning {
		t.Errorf("DE %+v, FI %+v; only DE was measured on its running client", byCode["DE"], byCode["FI"])
	}
	// Only Finland connected without having been offered.
	for code, r := range byCode {
		if r.New != (code == "FI") {
			t.Errorf("%s: new %v; only FI connected and was not known", code, r.New)
		}
	}

	if m.procOf("DE") != customersDE || !customersDE.IsRunning() || !connectedTo(m, "DE")() {
		t.Fatal("scanning DE touched the DE client carrying customers")
	}
	if _, err := os.Stat(filepath.Join(Root(), "DE", "config.json")); err != nil {
		t.Fatalf("the running DE's directory: %v", err)
	}
	// What a scan's client moves is the test's download, not customers' traffic.
	saved := map[string]Traffic{}
	collect(t, m, saved)
	if want := map[string]Traffic{"DE": oneStart}; !maps.Equal(saved, want) {
		t.Fatalf("traffic after the scan %v, want only the running DE's %v", saved, want)
	}

	starts := scanStarts(t, log)
	if got, want := startedFor(starts), []string{"FI", "HU", "IT", "US"}; !slices.Equal(got, want) {
		t.Fatalf("scan clients started for %v, want one for each country not running, %v", got, want)
	}
	dirs := map[string]bool{}
	for _, s := range starts {
		for _, port := range []int{s.socks, s.httpPort} {
			if port <= scanPortFirst+5 || port > scanPortLast {
				t.Errorf("%s's scan client was given port %d; want one past %d, at most %d", s.region, port, scanPortFirst+5, scanPortLast)
			}
		}
		dirs[s.dir] = true
	}
	if len(dirs) != len(starts) {
		t.Errorf("scan clients shared a data directory: %+v", starts)
	}
	leftNothingBehind(t, starts)

	// The next scan starts from scratch: a fresh directory, never the last one's.
	if err := m.StartScan(ScanPlan{Codes: []string{"FI"}, Taken: taken}); err != nil {
		t.Fatalf("a scan after the last one finished: %v", err)
	}
	if st := waitScanDone(t, m); len(st.Results) != 1 || !st.Results[0].Exit.Works() {
		t.Fatalf("the second scan: %+v", st)
	}
	again := scanStarts(t, log)
	if len(again) != 5 || dirs[again[4].dir] {
		t.Fatalf("the second scan's FI ran in %v, a directory used before", again[len(again)-1].dir)
	}
}

func startedFor(starts []fakeStart) []string {
	var out []string
	for _, s := range starts {
		out = append(out, s.region)
	}
	slices.Sort(out)
	return out
}

// The three-client cap is for memory, and a running country's client is already there:
// it is measured at once, even listed after the others, beside three new clients.
func TestARunningCountryNeedsNoneOfTheThreeClientSlots(t *testing.T) {
	log := fakeRegions(t, "SE", 1500*time.Millisecond)
	m := newManager()
	t.Cleanup(m.StopAll)
	se := Country{"SE", 11181, 11381}
	if err := m.Reconcile([]Country{se}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "SE to connect", connectedTo(m, "SE"))

	if err := m.StartScan(ScanPlan{Codes: []string{"AT", "BE", "CH", "DK", "SE"}, Running: []Country{se}}); err != nil {
		t.Fatal(err)
	}
	measuredSE := func(st ScanState) bool {
		return slices.ContainsFunc(st.Results, func(r ScanResult) bool { return r.Code == "SE" })
	}
	most := 0
	for {
		// The directories are read first: seen before SE's result, they were there before it.
		clients := len(scanDirs())
		st := m.ScanState()
		if measuredSE(st) || !st.Running {
			break
		}
		most = max(most, clients)
		time.Sleep(2 * time.Millisecond)
	}
	if most != 3 {
		t.Fatalf("%d new clients ran before SE was measured; want 3, SE taking none of their slots", most)
	}
	st := waitScanDone(t, m)
	if len(st.Results) != 5 || st.Results[0].Code != "SE" || !st.Results[0].OnRunning || !st.Results[0].Exit.Works() {
		t.Fatalf("results %+v; want SE first, measured on its running client", st.Results)
	}
	leftNothingBehind(t, scanStarts(t, log))
}

// Each client lists only the countries of the servers it was handed, and Psiphon adds
// countries no list here names: whatever a scan's client announces is tried in that scan.
func TestAScanTriesTheCountriesItsClientsAnnounce(t *testing.T) {
	log := fakeRegions(t, "FI,PT,EE,DE", 1500*time.Millisecond)
	t.Setenv(fakeListsEnv, "HU=HU+PT+KR;FI=FI+EE+PT+DE")
	m := newManager()
	t.Cleanup(m.StopAll)
	de := Country{"DE", 11181, 11381}
	if err := m.Reconcile([]Country{de}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	var finished []ScanState
	plan := ScanPlan{
		Codes:    []string{"FI", "HU"},
		Running:  []Country{de},
		Known:    []string{"DE", "FI", "HU"},
		Finished: func(st ScanState) { finished = append(finished, st) },
	}
	if err := m.StartScan(plan); err != nil {
		t.Fatal(err)
	}
	st := waitScanDone(t, m)

	byCode := map[string]ScanResult{}
	for _, r := range st.Results {
		if _, twice := byCode[r.Code]; twice {
			t.Errorf("%s was tried twice", r.Code)
		}
		byCode[r.Code] = r
	}
	tried := slices.Sorted(maps.Keys(byCode))
	if want := []string{"DE", "EE", "FI", "HU", "KR", "PT"}; !slices.Equal(tried, want) || st.Total != len(want) || st.Stopped {
		t.Fatalf("tried %v of total %d; want %v, the total grown to match", tried, st.Total, want)
	}
	for code, r := range byCode {
		works := code != "HU" && code != "KR"
		if r.Exit.Works() != works {
			t.Errorf("%s: %+v; want works=%v", code, r, works)
		}
		if r.New != (code == "PT" || code == "EE") {
			t.Errorf("%s: new %v; PT and EE connected and were known to nobody", code, r.New)
		}
	}
	if !byCode["DE"].OnRunning {
		t.Errorf("DE, announced while it ran, got a client of its own: %+v", byCode["DE"])
	}
	if got, want := startedFor(scanStarts(t, log)), []string{"EE", "FI", "HU", "KR", "PT"}; !slices.Equal(got, want) {
		t.Fatalf("scan clients started for %v, want %v", got, want)
	}
	if len(finished) != 1 || finished[0].Total != st.Total || len(finished[0].Results) != len(st.Results) || finished[0].Running {
		t.Fatalf("the plan was told of the end %d times: %+v; want once, with the final state", len(finished), finished)
	}
	leftNothingBehind(t, scanStarts(t, log))
}

// A 458 MB box running six clients beside the panel was killed for memory, so a
// scan never runs more than three at once, and still runs three side by side.
func TestAScanRunsAtMostThreeClientsAtOnce(t *testing.T) {
	log := fakeRegions(t, "", 800*time.Millisecond)
	m := newManager()
	t.Cleanup(m.StopAll)
	if err := m.StartScan(ScanPlan{Codes: []string{"AT", "BE", "CH", "DK", "ES", "IT", "PT"}}); err != nil {
		t.Fatal(err)
	}
	most := 0
	for m.ScanState().Running {
		most = max(most, len(scanDirs()))
		time.Sleep(5 * time.Millisecond)
	}
	if most != 3 {
		t.Fatalf("at most %d scan clients ran at once; want exactly 3", most)
	}
	st := m.ScanState()
	if len(st.Results) != 7 {
		t.Fatalf("%d of 7 countries reported", len(st.Results))
	}
	leftNothingBehind(t, scanStarts(t, log))
}

// The panel shutting down or going on standby stops the scan's clients and
// removes their directories before it goes on; the cut-off countries are not failures.
func TestStopAllCutsAScanShortAndLeavesNothing(t *testing.T) {
	log := fakeRegions(t, "", time.Minute)
	m := newManager()
	t.Cleanup(m.StopAll)
	told := false
	if err := m.StartScan(ScanPlan{Codes: []string{"AT", "BE", "CH", "DK", "ES"}, Finished: func(ScanState) { told = true }}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "three scan clients to start", func() bool { return len(scanStarts(t, log)) == 3 })
	starts := scanStarts(t, log)
	for _, s := range starts {
		waitFor(t, s.region+"'s client to listen", func() bool { return listening(s.httpPort) })
	}

	begun := time.Now()
	m.StopAll()
	if took := time.Since(begun); took > 10*time.Second {
		t.Fatalf("stopping took %v", took)
	}
	st := m.ScanState()
	if st.Running || !st.Stopped || len(st.Results) != 0 || st.Total != 5 {
		t.Fatalf("after the stop the scan reads %+v; want stopped with nothing reported", st)
	}
	leftNothingBehind(t, starts)
	if got := len(scanStarts(t, log)); got != 3 {
		t.Fatalf("%d clients started in all; the stop let the queue go on", got)
	}
	if told {
		t.Fatal("a scan cut short was handed on as finished")
	}
	if err := m.StartScan(ScanPlan{Codes: []string{"AT"}}); err != nil {
		t.Fatalf("a scan after the stopped one: %v", err)
	}
}

// The card's test reads every country already running through its own client;
// one whose tunnel is not up says why, and a country with no client is left out.
func TestTestCountriesMeasuresTheRunningClients(t *testing.T) {
	fakeRegions(t, "DE", time.Second)
	m := newManager()
	t.Cleanup(m.StopAll)
	de, us := Country{"DE", 11181, 11381}, Country{"US", 11182, 11382}
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	waitFor(t, "US to start", func() bool { return m.Status("US").Running })

	got := m.TestCountries(t.Context(), []Country{de, us, {"FR", 11183, 11383}})
	if len(got) != 2 || got[0].Code != "DE" || got[1].Code != "US" {
		t.Fatalf("tested %+v; want DE and US, not FR with no client", got)
	}
	if d := got[0]; !d.Connected || !d.Exit.Works() || d.Exit.IP != fakeExitIP || d.Exit.Loc != "DE" || d.Exit.BytesPerSec <= 0 || d.Exit.Latency < fakeTraceDelay {
		t.Errorf("DE: %+v; want a working exit with its latency and speed", d)
	}
	if u := got[1]; u.Connected || u.Exit.Err == nil || u.Exit.IP != "" {
		t.Errorf("US: %+v; its tunnel never came up", u)
	}

	// A tunnel too slow to finish the download in its budget is timed on what arrived.
	saved := speedBudget
	speedBudget = fakeDownPause / 2
	t.Cleanup(func() { speedBudget = saved })
	slow := m.TestCountries(t.Context(), []Country{de})
	if len(slow) != 1 || slow[0].Exit.Err != nil || slow[0].Exit.BytesPerSec <= 0 {
		t.Fatalf("a download cut short by its budget: %+v; want its speed, not a failure", slow)
	}

	// Time up before a country's turn says so, rather than pass for the country failing.
	over, cancel := context.WithCancel(t.Context())
	cancel()
	late := m.TestCountries(over, []Country{de, us})
	if len(late) != 2 || !errors.Is(late[0].Exit.Err, ErrNotReached) || !errors.Is(late[1].Exit.Err, ErrNotReached) {
		t.Fatalf("countries the test never reached: %+v; want ErrNotReached", late)
	}
}
