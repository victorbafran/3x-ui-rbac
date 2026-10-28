package psiphon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

const fakeEnv = "XUI_FAKE_PSIPHON"

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
		EgressRegion        string
		LocalSocksProxyPort int
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
	case "silent":
		fmt.Println(notice("AvailableEgressRegions", `{"regions":[]}`))
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
