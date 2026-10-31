package service

import (
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

// A scan tries every country the card lists, Finland and the ones only the probe
// or an old row knows included, enabled or not; a standby copy runs none.
func TestAScanTriesEveryListedCountry(t *testing.T) {
	setupRelayDB(t)
	installFakePsiphon(t)
	t.Cleanup(psiphon.GetManager().StopScan)
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE","ZZ"],"at":1}`); err != nil {
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
		if r.Connected || r.Error == "" {
			t.Errorf("%s: %+v; a client that cannot run must say why it did not connect", r.Code, r)
		}
	}
	slices.Sort(tried)
	want := append(slices.Clone(psiphon.FallbackRegions), "XK", "ZZ")
	slices.Sort(want)
	if !slices.Equal(tried, want) || st.Scan.Total != len(want) || st.Scan.Running || st.Scan.StartedAt == 0 || st.Scan.FinishedAt == 0 {
		t.Fatalf("scanned %v (%+v); want %v", tried, st.Scan, want)
	}
	if !slices.Contains(tried, "FI") {
		t.Fatal("Finland was not scanned")
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
