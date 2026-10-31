package psiphon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

const (
	fakeEnv = "XUI_FAKE_PSIPHON"
	// fakeUpEnv lists the countries the "regions" stand-in connects to; fakeLogEnv
	// is a file each start appends its country, ports and data directory to.
	fakeUpEnv  = "XUI_FAKE_PSIPHON_UP"
	fakeLogEnv = "XUI_FAKE_PSIPHON_LOG"
)

// TestMain lets the test binary stand in for Psiphon's client: with
// XUI_FAKE_PSIPHON set it reads its config and prints notices like the real one.
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeEnv); scenario != "" {
		os.Exit(fakeClient(scenario, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func notice(kind, data string) string {
	return `{"data":` + data + `,"noticeType":"` + kind + `","timestamp":"2026-10-28T15:04:05.123Z"}`
}

func fakeClient(scenario string, args []string) int {
	var cfgPath, dataDir string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-config":
			cfgPath = args[i+1]
		case "-dataRootDirectory":
			dataDir = args[i+1]
		}
	}
	var cfg struct {
		EgressRegion         string
		LocalSocksProxyPort  int
		LocalHTTPProxyPort   int `json:"LocalHttpProxyPort"`
		EmitBytesTransferred bool
	}
	raw, err := os.ReadFile(cfgPath)
	if err == nil {
		err = json.Unmarshal(raw, &cfg)
	}
	if err != nil {
		fmt.Println("fake psiphon: no config:", err)
		return 1
	}
	switch scenario {
	case "tunnel":
		fmt.Println(notice("ListeningSocksProxyPort", fmt.Sprintf(`{"port":%d}`, cfg.LocalSocksProxyPort)))
		fmt.Println(notice("ConnectedServerRegion", fmt.Sprintf(`{"serverRegion":%q}`, cfg.EgressRegion)))
		// Like the real client, counts only when asked to; before Tunnels, so a
		// test that saw the tunnel come up has seen these too.
		if cfg.EmitBytesTransferred {
			fmt.Println(notice("BytesTransferred", `{"diagnosticID":"NrH6n4lc","received":2524300,"sent":722}`))
			fmt.Println(notice("BytesTransferred", `{"diagnosticID":"NrH6n4lc","received":1048576,"sent":4096}`))
		}
		fmt.Println(notice("Tunnels", `{"count":1}`))
	case "probe":
		if cfg.EgressRegion != "" {
			fmt.Println("fake psiphon: a probe pinned to", cfg.EgressRegion, "lists one country")
			return 1
		}
		list := filepath.Join(dataDir, "ca.psiphon.PsiphonTunnel.tunnel-core", "remote_server_list")
		if _, err := os.Stat(list); err == nil {
			fmt.Println(notice("AvailableEgressRegions", `{"regions":["US","NL","DE"]}`))
			break
		}
		fmt.Println(notice("AvailableEgressRegions", `{"regions":[]}`))
		fmt.Println(notice("Tunnels", `{"count":1}`))
		_ = os.MkdirAll(filepath.Dir(list), 0o750)
		_ = os.WriteFile(list, []byte("servers"), 0o600)
	case "partial":
		// Measured 2026-10-28: a fresh directory lists a few countries; the next
		// start announces the whole list between short ones.
		list := filepath.Join(dataDir, "ca.psiphon.PsiphonTunnel.tunnel-core", "remote_server_list")
		if _, err := os.Stat(list); err == nil {
			fmt.Println(notice("AvailableEgressRegions", `{"regions":["DE"]}`))
			time.Sleep(300 * time.Millisecond)
			fmt.Println(notice("AvailableEgressRegions", `{"regions":["US","PL","JP","DE","AT"]}`))
			fmt.Println(notice("AvailableEgressRegions", `{"regions":["DE"]}`))
			break
		}
		fmt.Println(notice("AvailableEgressRegions", `{"regions":["NL","DE"]}`))
		fmt.Println(notice("Tunnels", `{"count":1}`))
		time.Sleep(300 * time.Millisecond)
		_ = os.MkdirAll(filepath.Dir(list), 0o750)
		_ = os.WriteFile(list, []byte("servers"), 0o600)
	case "silent":
		fmt.Println(notice("AvailableEgressRegions", `{"regions":[]}`))
	case "regions":
		logFakeStart(cfg.EgressRegion, cfg.LocalSocksProxyPort, cfg.LocalHTTPProxyPort, dataDir)
		// The HTTP port is held either way, so a test can tell the process is gone.
		if err := listenFake(cfg.LocalHTTPProxyPort, nil); err != nil {
			fmt.Println("fake psiphon:", err)
			return 1
		}
		if !slices.Contains(strings.Split(os.Getenv(fakeUpEnv), ","), cfg.EgressRegion) {
			fmt.Println(notice("Tunnels", `{"count":0}`))
			break
		}
		if err := listenFake(cfg.LocalSocksProxyPort, func(c net.Conn) { fakeExit(c, cfg.EgressRegion) }); err != nil {
			fmt.Println("fake psiphon:", err)
			return 1
		}
		fmt.Println(notice("ConnectedServerRegion", fmt.Sprintf(`{"serverRegion":%q}`, cfg.EgressRegion)))
		if cfg.EmitBytesTransferred {
			fmt.Println(notice("BytesTransferred", `{"diagnosticID":"NrH6n4lc","received":2524300,"sent":722}`))
			fmt.Println(notice("BytesTransferred", `{"diagnosticID":"NrH6n4lc","received":1048576,"sent":4096}`))
		}
		fmt.Println(notice("Tunnels", `{"count":1}`))
	}
	time.Sleep(time.Minute)
	return 0
}

// useFakeClient points the package at the stand-in; the returned pointer is
// the binary path, which a test may move to a "new build".
func useFakeClient(t *testing.T, scenario string) *string {
	t.Helper()
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	t.Setenv(fakeEnv, scenario)
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	bin := &self
	saved := resolveBinary
	resolveBinary = func() (string, error) { return *bin, nil }
	t.Cleanup(func() { resolveBinary = saved })
	return bin
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (m *Manager) procOf(code string) *sidecar.Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.procs[code]; cur != nil {
		return cur.proc
	}
	return nil
}

func connectedTo(m *Manager, code string) func() bool {
	return func() bool {
		s := m.Status(code)
		return s.Running && s.Connected && s.Region == code
	}
}

// The job reconciles every 30 s and a restart cuts every customer on that
// country, so only a change the client reads, or a dead client, may restart it.
func TestReconcileRestartsOnlyWhatChanged(t *testing.T) {
	bin := useFakeClient(t, "tunnel")
	m := newManager()
	t.Cleanup(m.StopAll)
	de, us := Country{"DE", 11181, 11381}, Country{"US", 11182, 11382}

	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	waitFor(t, "US to connect", connectedTo(m, "US"))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(Root(), "DE", "config.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("DE's config: %v, %v; want mode 600", info, err)
		}
	}

	firstDE, firstUS := m.procOf("DE"), m.procOf("US")
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if m.procOf("DE") != firstDE || m.procOf("US") != firstUS {
		t.Fatal("an unchanged reconcile restarted a client")
	}

	us.SocksPort = 11183
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile with US on a new port: %v", err)
	}
	if m.procOf("DE") != firstDE {
		t.Fatal("moving US's port restarted DE")
	}
	if m.procOf("US") == firstUS || firstUS.IsRunning() {
		t.Fatal("US kept its old process after its port moved")
	}
	waitFor(t, "US to reconnect", connectedTo(m, "US"))

	newBuild := filepath.Join(t.TempDir(), filepath.Base(*bin))
	copyFile(t, *bin, newBuild)
	*bin = newBuild
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile onto a new build: %v", err)
	}
	if m.procOf("DE") == firstDE || firstDE.IsRunning() {
		t.Fatal("DE kept running the old build")
	}

	dead := m.procOf("DE")
	_ = dead.Stop()
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile after DE died: %v", err)
	}
	if m.procOf("DE") == dead || !m.procOf("DE").IsRunning() {
		t.Fatal("a client that died was not started again")
	}
	waitFor(t, "DE to reconnect", connectedTo(m, "DE"))

	gone := m.procOf("US")
	if err := m.Reconcile([]Country{de}); err != nil {
		t.Fatalf("reconcile without US: %v", err)
	}
	if gone.IsRunning() || m.Status("US") != (Status{}) {
		t.Fatalf("US is still managed after it was dropped: %+v", m.Status("US"))
	}

	m.StopAll()
	if m.HasRunning() || m.Status("DE").Running {
		t.Fatal("StopAll left a client running")
	}
}

// A code becomes a directory name, so one that is not a country never reaches
// the disk; a bad edit to a running country leaves it carrying its customers.
func TestReconcileRefusesWhatCannotBeAClient(t *testing.T) {
	useFakeClient(t, "tunnel")
	m := newManager()
	t.Cleanup(m.StopAll)
	us := Country{"US", 11182, 11382}
	if err := m.Reconcile([]Country{us}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	running := m.procOf("US")

	err := m.Reconcile([]Country{
		{"../x", 11190, 11390},
		{"DE", 11181, 11381},
		{"DE", 11184, 11384},
		{"US", 11181, 11382},
	})
	for _, want := range []string{`"../x" is not a two-letter`, "DE is listed twice", "DE and US both want port 11181"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not say %q", err, want)
		}
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	if m.procOf("US") != running || !running.IsRunning() {
		t.Fatal("a refused edit took down the US client")
	}
	entries, _ := os.ReadDir(Root())
	var dirs []string
	for _, e := range entries {
		dirs = append(dirs, e.Name())
	}
	if strings.Join(dirs, ",") != "DE,US" {
		t.Fatalf("psiphon root holds %v, want only DE and US", dirs)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(Root()), "x")); !os.IsNotExist(err) {
		t.Fatalf("a code with a path in it wrote outside the psiphon root: %v", err)
	}
}

// oneStart is what the stand-in reports carrying each time it starts.
var oneStart = Traffic{Down: 2524300 + 1048576, Up: 722 + 4096}

func times(n int64, t Traffic) Traffic { return Traffic{Down: n * t.Down, Up: n * t.Up} }

// collect flushes m into saved, as the job's save adds to each country's row.
func collect(t *testing.T, m *Manager, saved map[string]Traffic) {
	t.Helper()
	err := m.FlushTraffic(func(moved map[string]Traffic) error {
		for code, by := range moved {
			sum := saved[code]
			saved[code] = Traffic{Down: sum.Down + by.Down, Up: sum.Up + by.Up}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// The notices are the only count of what customers moved through a country, so
// a client restarting or switched off, or a save that fails, must not lose them.
func TestTrafficOutlivesItsClientAndAFailedSave(t *testing.T) {
	useFakeClient(t, "tunnel")
	m := newManager()
	t.Cleanup(m.StopAll)
	de, us := Country{"DE", 11181, 11381}, Country{"US", 11182, 11382}
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	waitFor(t, "DE to connect", connectedTo(m, "DE"))
	waitFor(t, "US to connect", connectedTo(m, "US"))

	first := m.procOf("DE")
	de.SocksPort = 11183
	if err := m.Reconcile([]Country{de, us}); err != nil {
		t.Fatalf("reconcile with DE on a new port: %v", err)
	}
	if m.procOf("DE") == first {
		t.Fatal("DE was not restarted")
	}
	waitFor(t, "DE to reconnect", connectedTo(m, "DE"))
	if err := m.Reconcile([]Country{de}); err != nil {
		t.Fatalf("reconcile without US: %v", err)
	}

	err := m.FlushTraffic(func(map[string]Traffic) error { return errors.New("database is locked") })
	if err == nil || err.Error() != "database is locked" {
		t.Fatalf("a failed save returned %v", err)
	}
	saved := map[string]Traffic{}
	collect(t, m, saved)
	want := map[string]Traffic{"DE": times(2, oneStart), "US": oneStart}
	if !maps.Equal(saved, want) {
		t.Fatalf("saved %v, want %v: both DE processes and the switched-off US", saved, want)
	}
	collect(t, m, saved)
	if !maps.Equal(saved, want) {
		t.Fatalf("a second flush with nothing new saved %v, want still %v", saved, want)
	}
}

// A reset starts the count from that moment: bytes counted before it but not
// yet saved are dropped, for the countries it names only, once the zeroing held.
func TestAResetDropsWhatItsCountriesCountedBeforeIt(t *testing.T) {
	m := newManager()
	both := func() {
		m.traffic.add("DE", oneStart)
		m.traffic.add("US", oneStart)
	}
	saved := map[string]Traffic{}

	both()
	if err := m.ResetTraffic([]string{"DE"}, func() error { return errors.New("database is locked") }); err == nil {
		t.Fatal("a reset whose zeroing failed reported success")
	}
	collect(t, m, saved)
	if want := map[string]Traffic{"DE": oneStart, "US": oneStart}; !maps.Equal(saved, want) {
		t.Fatalf("after a failed reset saved %v, want %v", saved, want)
	}

	clear(saved)
	both()
	if err := m.ResetTraffic([]string{"DE"}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	collect(t, m, saved)
	if want := map[string]Traffic{"US": oneStart}; !maps.Equal(saved, want) {
		t.Fatalf("after resetting DE saved %v, want %v", saved, want)
	}

	clear(saved)
	both()
	if err := m.ResetTraffic(nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	collect(t, m, saved)
	if len(saved) != 0 {
		t.Fatalf("after resetting every country saved %v, want nothing", saved)
	}

	// A reset pressed while the job saves waits for it, or the bytes the
	// save took from before the reset would land after the zeroing.
	db := map[string]Traffic{}
	resetDone := make(chan error, 1)
	m.traffic.add("DE", oneStart)
	err := m.FlushTraffic(func(moved map[string]Traffic) error {
		go func() { resetDone <- m.ResetTraffic(nil, func() error { clear(db); return nil }) }()
		time.Sleep(100 * time.Millisecond)
		maps.Copy(db, moved)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if len(db) != 0 {
		t.Fatalf("a reset during a save left %v counted", db)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
