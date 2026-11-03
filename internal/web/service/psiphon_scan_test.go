package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// installFakePsiphon puts a file where the pinned client is cached, so the panel reads
// Psiphon as installed, and points every client at it: a file no system can run.
func installFakePsiphon(t *testing.T) {
	t.Helper()
	bin := filepath.Join(config.GetDBFolderPath(), "tools", "psiphon-"+psiphon.BinaryCommit[:12], "x-ui-psiphon-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("client"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(psiphon.UseBinaryForTest(bin))
	saved := psiphonRunsHere
	psiphonRunsHere = func() error { return nil }
	t.Cleanup(func() { psiphonRunsHere = saved })
}

// Measured 2026-10-31: a client pinned to Finland connected and left from Finland,
// though no list Psiphon announced carried it; the operator must be able to pick it.
func TestFinlandCanBeSwitchedOnThoughNoListCarriedIt(t *testing.T) {
	setupRelayDB(t)
	saved := psiphonRunsHere
	psiphonRunsHere = func() error { return errors.New("no client in tests") }
	t.Cleanup(func() { psiphonRunsHere = saved })
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE","NL"],"at":1}`); err != nil {
		t.Fatal(err)
	}
	if err := (&PsiphonService{}).SetCountries([]string{"fi"}); err != nil {
		t.Fatalf("switching Finland on: %v", err)
	}
	var row model.PsiphonCountry
	if err := database.GetDB().Where("code = ?", "FI").First(&row).Error; err != nil || !row.Enabled {
		t.Fatalf("Finland's row %+v, %v; want it enabled", row, err)
	}
}

// A scan tries every country the card lists, Finland and the ones only the probe, an
// earlier scan or an old row knows included, enabled or not, and the candidates no list
// names; a standby copy runs none, and a country that failed this time stays offered.
func TestAScanTriesEveryListedCountry(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	t.Cleanup(psiphon.GetManager().StopScan)
	const setting = `{"regions":["DE","ZZ"],"at":1,"found":["YY"]}`
	if err := (&SettingService{}).setString(psiphonRegionsKey, setting); err != nil {
		t.Fatal(err)
	}
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "XK", Enabled: false, SocksPort: 11181, HTTPPort: 11381},
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11182, HTTPPort: 11382},
	)
	svc := &PsiphonService{}

	before := psiphon.GetManager().ScanState().Started
	global.SetStandby(true)
	err := svc.StartScan()
	global.SetStandby(false)
	if !errors.Is(err, global.ErrStandby) {
		t.Fatalf("a scan on a standby copy: %v", err)
	}
	if st := psiphon.GetManager().ScanState(); !st.Started.Equal(before) {
		t.Fatalf("the refused scan started clients: %+v", st)
	}

	if err := svc.StartScan(); err != nil {
		t.Fatalf("start the scan: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for psiphon.GetManager().ScanState().Running {
		if time.Now().After(deadline) {
			t.Fatal("the scan did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	var tried []string
	for _, r := range st.Scan.Results {
		tried = append(tried, r.Code)
		// DE is enabled, but with no client running it gets one of its own like the rest.
		if r.Connected || r.Error == "" || r.OnRunning || r.New {
			t.Errorf("%s: %+v; a client that cannot run must say why it did not connect", r.Code, r)
		}
	}
	slices.Sort(tried)
	want := append(slices.Clone(psiphon.FallbackRegions), "XK", "ZZ", "YY")
	want = append(want, psiphon.CandidateRegions...)
	slices.Sort(want)
	if !slices.Equal(tried, want) || st.Scan.Total != len(want) || st.Scan.Running || st.Scan.StartedAt == 0 || st.Scan.FinishedAt == 0 {
		t.Fatalf("scanned %v (%+v); want %v", tried, st.Scan, want)
	}
	if !slices.Contains(tried, "FI") {
		t.Fatal("Finland was not scanned")
	}
	if !slices.Contains(st.Regions, "YY") || slices.Contains(st.Regions, "KR") {
		t.Fatalf("offered after a scan where nothing connected: %v; want YY kept and no candidate added", st.Regions)
	}
	if raw, err := (&SettingService{}).getString(psiphonRegionsKey); err != nil || raw != setting {
		t.Fatalf("the saved list after a scan where nothing connected: %s, %v; want it untouched", raw, err)
	}
}

func waitForPsiphonScan(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for psiphon.GetManager().ScanState().Running {
		if time.Now().After(deadline) {
			t.Fatal("the scan did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A panel installed today offers no country at first, not the announced ones nor the
// known 27. A scan tries them all and the candidates, and only those that connected join
// the list; a refresh of the announced countries leaves the list as the scan made it.
func TestAFreshPanelListsOnlyWhatAScanFound(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	t.Cleanup(psiphon.GetManager().StopScan)
	svc := &PsiphonService{}
	discover := func(regions ...string) {
		t.Helper()
		saved := discoverPsiphonRegions
		discoverPsiphonRegions = func(context.Context) ([]string, error) { return regions, nil }
		defer func() { discoverPsiphonRegions = saved }()
		if err := svc.discoverRegions(t.Context()); err != nil {
			t.Fatalf("read the countries Psiphon announces: %v", err)
		}
	}
	discover("DE", "NL", "ZZ")
	st, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Regions) != 0 || len(st.Countries) != 0 {
		t.Fatalf("a fresh panel lists %v and %d countries before any scan; want none", st.Regions, len(st.Countries))
	}

	t.Cleanup(psiphon.AnswerScanForTest(func(code string) psiphon.ScanResult {
		switch code {
		case "DE", "KR":
			return psiphon.ScanResult{Connected: true, Exit: psiphon.Exit{IP: "203.0.113.7", Loc: code, BytesPerSec: 1 << 20}}
		}
		return psiphon.ScanResult{Detail: "no tunnel"}
	}))
	if err := svc.StartScan(); err != nil {
		t.Fatal(err)
	}
	waitForPsiphonScan(t)
	want := append(slices.Clone(psiphon.FallbackRegions), psiphon.CandidateRegions...)
	want = append(want, "ZZ")
	if st, err = svc.State(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Regions, []string{"DE", "KR"}) {
		t.Fatalf("listed after a scan where DE and KR connected: %v; want just those", st.Regions)
	}
	if st.Scan.Total != len(want) {
		t.Fatalf("the scan tried %d countries; want all %d the panel knows", st.Scan.Total, len(want))
	}
	for _, c := range st.Countries {
		if c.Code == "DE" && (c.Name != "Germany" || c.Flag == "") {
			t.Errorf("DE listed as %q %q; want its name and flag", c.Flag, c.Name)
		}
	}

	discover("NL", "PL")
	if st, err = svc.State(); err != nil || !slices.Equal(st.Regions, []string{"DE", "KR"}) {
		t.Fatalf("after reading the announced countries again: %v, %v; want the scan's list untouched", st.Regions, err)
	}

	saveRuns := psiphonRunsHere
	psiphonRunsHere = func() error { return errors.New("no client in tests") }
	t.Cleanup(func() { psiphonRunsHere = saveRuns })
	if err := svc.SetCountries([]string{"KR"}); err != nil {
		t.Fatalf("switching on a country the scan found: %v", err)
	}
	if err := svc.SetCountries([]string{"KR", "NL"}); err == nil || !strings.Contains(err.Error(), "does not offer NL") {
		t.Fatalf("switching on NL, which no scan found: %v; want it refused", err)
	}
}

// Even when the announced countries were never read, the first scan's finds make a list of
// their own: saving them must not turn the panel into one that lists the known 27.
func TestAFirstScanFindWithNoSavedListStaysAScanList(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	svc := &PsiphonService{}
	svc.keepScanFinds(psiphonScanOf(psiphon.ScanResult{Code: "KR", Connected: true}, psiphon.ScanResult{Code: "DE", Connected: true}))
	st, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Regions, []string{"DE", "KR"}) {
		t.Fatalf("listed after the first find: %v; want just DE and KR", st.Regions)
	}
}

// A panel that already listed the announced and known countries keeps them.
func TestAPanelThatAlreadyHadAListKeepsIt(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE"],"at":1}`); err != nil {
		t.Fatal(err)
	}
	st, err := (&PsiphonService{}).State()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range append([]string{"DE"}, psiphon.FallbackRegions...) {
		if !slices.Contains(st.Regions, code) {
			t.Fatalf("an existing panel lost %s from its list: %v", code, st.Regions)
		}
	}
}

// A scan's finds reach the offered list by themselves: KR and PT, candidates no list
// carried, connect and are kept and marked new; FI and DE, offered already, are not new.
func TestAScanKeepsTheCountriesThatConnected(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	t.Cleanup(psiphon.GetManager().StopScan)
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE"],"at":1}`); err != nil {
		t.Fatal(err)
	}
	seedPsiphonCountries(t, model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381})
	t.Cleanup(psiphon.AnswerScanForTest(func(code string) psiphon.ScanResult {
		switch code {
		case "KR", "PT", "FI", "DE":
			return psiphon.ScanResult{Connected: true, Exit: psiphon.Exit{IP: "203.0.113.7", Loc: code, BytesPerSec: 1 << 20}}
		}
		return psiphon.ScanResult{Detail: "no tunnel"}
	}))
	svc := &PsiphonService{}
	if err := svc.StartScan(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for psiphon.GetManager().ScanState().Running {
		if time.Now().After(deadline) {
			t.Fatal("the scan did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}

	saved, err := svc.savedRegions()
	if err != nil || !slices.Equal(saved.Found, []string{"KR", "PT"}) {
		t.Fatalf("saved %+v, %v; want KR and PT found", saved, err)
	}
	st, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Regions, "KR") || !slices.Contains(st.Regions, "PT") || slices.Contains(st.Regions, "HK") {
		t.Fatalf("offered %v; want KR and PT, not HK, which did not connect", st.Regions)
	}
	for _, r := range st.Scan.Results {
		if r.New != (r.Code == "KR" || r.Code == "PT") {
			t.Errorf("%s: new %v; only KR and PT were not offered before", r.Code, r.New)
		}
	}
}

func psiphonScanOf(results ...psiphon.ScanResult) psiphon.ScanState {
	return psiphon.ScanState{Started: time.Unix(1793450000, 0), Finished: time.Unix(1793450600, 0), Total: len(results), Results: results}
}

// Psiphon adds countries no list here names; one a scan saw connect is offered from then
// on, named and flagged, and switchable on; a refresh of the probe's list keeps it.
func TestCountriesAScanFoundStayOffered(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	svc := &PsiphonService{}
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE"],"at":1,"found":["HK"]}`); err != nil {
		t.Fatal(err)
	}
	works := psiphon.Exit{IP: "203.0.113.7", Loc: "KR", Latency: 300 * time.Millisecond, BytesPerSec: 1 << 20}
	scan := psiphonScanOf(
		psiphon.ScanResult{Code: "KR", Connected: true, Exit: works, New: true},
		psiphon.ScanResult{Code: "PT", Connected: true, Exit: psiphon.Exit{IP: "203.0.113.8", Err: errors.New("the download through Psiphon answered 403 Forbidden")}, New: true},
		psiphon.ScanResult{Code: "HK"},
		psiphon.ScanResult{Code: "ZA"},
		psiphon.ScanResult{Code: "DE", Connected: true, Exit: works, OnRunning: true},
	)

	global.SetStandby(true)
	svc.keepScanFinds(scan)
	global.SetStandby(false)
	if saved, err := svc.savedRegions(); err != nil || !slices.Equal(saved.Found, []string{"HK"}) {
		t.Fatalf("a standby copy saved the scan's finds: %+v, %v", saved, err)
	}

	svc.keepScanFinds(scan)
	saved, err := svc.savedRegions()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"HK", "KR", "PT"}; !slices.Equal(saved.Found, want) || !slices.Equal(saved.Regions, []string{"DE"}) {
		t.Fatalf("saved %+v; want found %v beside the probe's DE", saved, want)
	}
	st, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Regions, "KR") || !slices.Contains(st.Regions, "HK") || slices.Contains(st.Regions, "ZA") {
		t.Fatalf("offered %v; want KR and HK, never ZA, which did not connect", st.Regions)
	}
	for _, c := range st.Countries {
		if c.Code == "KR" && (c.Name != "South Korea" || c.Flag != "🇰🇷") {
			t.Errorf("KR listed as %q %q; want its name and flag", c.Flag, c.Name)
		}
	}

	saveRuns := psiphonRunsHere
	psiphonRunsHere = func() error { return errors.New("no client in tests") }
	t.Cleanup(func() { psiphonRunsHere = saveRuns })
	if err := svc.SetCountries([]string{"KR"}); err != nil {
		t.Fatalf("switching on KR, which a scan found: %v", err)
	}

	saveDiscover := discoverPsiphonRegions
	discoverPsiphonRegions = func(context.Context) ([]string, error) { return []string{"NL"}, nil }
	t.Cleanup(func() { discoverPsiphonRegions = saveDiscover })
	if err := svc.discoverRegions(t.Context()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if saved, err := svc.savedRegions(); err != nil || !slices.Equal(saved.Regions, []string{"NL"}) || !slices.Equal(saved.Found, []string{"HK", "KR", "PT"}) {
		t.Fatalf("after a refresh %+v, %v; want the probe's NL and every country a scan found", saved, err)
	}

	view := psiphonScanView(scan)
	if r := view.Results[0]; !r.New || r.OnRunning || r.Code != "KR" || r.Speed != 1<<20 {
		t.Errorf("the card's KR %+v; want it new, with its speed", r)
	}
	if r := view.Results[4]; r.New || !r.OnRunning {
		t.Errorf("the card's DE %+v; want it measured on its running client", r)
	}
}

// A country a scan finds is named on the card, not shown as its bare code.
func TestEveryCandidateCountryHasAName(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	// a panel that already had a list, so the known countries are on it beside the finds
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":[],"at":1}`); err != nil {
		t.Fatal(err)
	}
	var all []psiphon.ScanResult
	for _, code := range psiphon.CandidateRegions {
		all = append(all, psiphon.ScanResult{Code: code, Connected: true})
	}
	(&PsiphonService{}).keepScanFinds(psiphonScanOf(all...))
	st, err := (&PsiphonService{}).State()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]PsiphonCountryView{}
	for _, c := range st.Countries {
		listed[c.Code] = c
	}
	for _, code := range append(slices.Clone(psiphon.FallbackRegions), psiphon.CandidateRegions...) {
		if c, ok := listed[code]; !ok || c.Name == code || c.Name == "" {
			t.Errorf("%s listed as %+v; want it offered under its name", code, c)
		}
	}
}

// The card's test keeps each reading for the card and saves the exit as the job's
// check does: a trace that answered is the exit even when the download then failed.
func TestTheCardsTestSavesEachExitItFound(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381, ExitIP: "198.51.100.1", ExitLoc: "DE", LastError: "old"},
		model.PsiphonCountry{Code: "US", Enabled: true, SocksPort: 11182, HTTPPort: 11382, ExitIP: "198.51.100.2", ExitLoc: "US"},
		model.PsiphonCountry{Code: "GB", Enabled: true, SocksPort: 11183, HTTPPort: 11383},
		model.PsiphonCountry{Code: "NL", Enabled: true, SocksPort: 11184, HTTPPort: 11384, ExitIP: "198.51.100.4", ExitLoc: "NL", LastCheck: 5},
	)
	at := time.Unix(1793450000, 0)
	got := recordPsiphonTests([]psiphon.CountryTest{
		{Code: "DE", Connected: true, Exit: psiphon.Exit{IP: "164.92.130.120", Loc: "DE", Latency: 412 * time.Millisecond, BytesPerSec: 1_250_000}},
		{Code: "US", Exit: psiphon.Exit{Err: errors.New("socks connect tcp 127.0.0.1:11182: connection refused")}},
		{Code: "GB", Connected: true, Exit: psiphon.Exit{IP: "203.0.113.9", Loc: "GB", Latency: time.Second, Err: errors.New("the download through Psiphon answered 403 Forbidden")}},
		{Code: "NL", Connected: true, Exit: psiphon.Exit{Err: psiphon.ErrNotReached}},
	}, at)
	want := []PsiphonExitTest{
		{Code: "DE", Connected: true, ExitIP: "164.92.130.120", ExitLoc: "DE", LatencyMs: 412, Speed: 1_250_000, At: at.Unix()},
		{Code: "US", Error: "socks connect tcp 127.0.0.1:11182: connection refused", At: at.Unix()},
		{Code: "GB", Connected: true, ExitIP: "203.0.113.9", ExitLoc: "GB", LatencyMs: 1000, Error: "the download through Psiphon answered 403 Forbidden", At: at.Unix()},
		{Code: "NL", Connected: true, Error: psiphon.ErrNotReached.Error(), At: at.Unix()},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tests %+v\nwant %+v", got, want)
	}

	rows := map[string]model.PsiphonCountry{}
	var all []model.PsiphonCountry
	if err := database.GetDB().Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		rows[r.Code] = r
	}
	if r := rows["DE"]; r.ExitIP != "164.92.130.120" || r.LastError != "" || r.LastCheck != at.Unix() {
		t.Errorf("DE saved %+v; want its new exit and no error", r)
	}
	if r := rows["US"]; r.ExitIP != "198.51.100.2" || !strings.Contains(r.LastError, "connection refused") {
		t.Errorf("US saved %+v; want its old exit kept and the failure said", r)
	}
	if r := rows["GB"]; r.ExitIP != "203.0.113.9" || r.LastError != "" {
		t.Errorf("GB saved %+v; the trace answered, so that is its exit", r)
	}
	if r := rows["NL"]; r.ExitIP != "198.51.100.4" || r.LastError != "" || r.LastCheck != 5 {
		t.Errorf("NL saved %+v; the test never reached it, so its row must stay as it was", r)
	}

	st, err := (&PsiphonService{}).State()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Countries {
		if c.Code == "DE" && (c.Test == nil || *c.Test != want[0]) {
			t.Errorf("the card shows DE's test as %+v; want %+v", c.Test, want[0])
		}
	}
}

// A scan client holds two loopback ports for a minute or two; an inbound saved on one
// meanwhile would leave Xray unable to bind, and with it every other inbound.
func TestAnInboundCannotTakeAPortAScanHolds(t *testing.T) {
	setupRelayDB(t)
	release := psiphon.HoldScanPortForTest(11785)
	svc := &InboundService{}
	ib := &model.Inbound{Tag: "scan-port", Listen: "0.0.0.0", Port: 11785, Protocol: model.VLESS}
	if got, err := svc.checkPortConflict(ib, 0); err != nil || got == nil || got.Tag != "psiphon-scan" {
		t.Fatalf("an inbound on a port the scan holds: %+v, %v; want a psiphon-scan conflict", got, err)
	}
	elsewhere := &model.Inbound{Tag: "scan-port-lan", Listen: "10.0.0.1", Port: 11785, Protocol: model.VLESS}
	if got, err := svc.checkPortConflict(elsewhere, 0); err != nil || got != nil {
		t.Fatalf("an inbound on another address than loopback: %+v, %v; want it allowed", got, err)
	}
	release()
	if got, err := svc.checkPortConflict(ib, 0); err != nil || got != nil {
		t.Fatalf("the port once the scan let it go: %+v, %v; want it allowed", got, err)
	}
}
