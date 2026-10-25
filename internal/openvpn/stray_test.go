package openvpn

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

const ownConfig = "/var/lib/x-ui/openvpn/openvpn-3.conf"

// Only an openvpn running exactly this inbound's config is its server; a
// neighbour inbound, an editor or a shell mentioning the file are not.
func TestOnlyItsOwnConfigMakesAServer(t *testing.T) {
	for _, tc := range []struct {
		cmdline string
		want    bool
	}{
		{"/usr/sbin/openvpn\x00--config\x00" + ownConfig + "\x00", true},
		{"openvpn\x00--verb\x003\x00--config\x00" + ownConfig + "\x00", true},
		{"/usr/sbin/openvpn\x00--config\x00/var/lib/x-ui/openvpn/openvpn-33.conf\x00", false},
		{"/usr/sbin/openvpn\x00--config\x00", false},
		{"/usr/bin/vim\x00--config\x00" + ownConfig + "\x00", false},
		{"bash\x00-c\x00openvpn --config " + ownConfig + "\x00", false},
		{"", false},
	} {
		if got := serves([]byte(tc.cmdline), ownConfig); got != tc.want {
			t.Errorf("serves(%q) = %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	saved := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = saved })
	return root
}

func writeProc(t *testing.T, root, pid, file, body string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAStrayIsFoundAmongTheProcesses(t *testing.T) {
	root := fakeProc(t)
	writeProc(t, root, "100", "cmdline", "/usr/sbin/openvpn\x00--config\x00"+ownConfig+"\x00")
	writeProc(t, root, "200", "cmdline", "/usr/sbin/openvpn\x00--config\x00/var/lib/x-ui/openvpn/openvpn-4.conf\x00")
	writeProc(t, root, "self", "cmdline", "/usr/sbin/openvpn\x00--config\x00"+ownConfig+"\x00")
	if err := os.MkdirAll(filepath.Join(root, "300"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := strays(ownConfig); !slices.Equal(got, []int{100}) {
		t.Errorf("strays = %v, want only pid 100", got)
	}
}

// A process that has exited but is not yet reaped no longer holds the device.
func TestAZombieCountsAsGone(t *testing.T) {
	root := fakeProc(t)
	writeProc(t, root, "400", "stat", "400 (openvpn) Z 1 400 400 0 -1")
	writeProc(t, root, "401", "stat", "401 (vpn) Z x) S 1 401 401 0 -1")
	if !gone(400) {
		t.Error("a zombie was taken for a live process")
	}
	if gone(401) {
		t.Error("a sleeping process was taken for gone")
	}
	if !gone(402) {
		t.Error("a pid with no entry was taken for a live process")
	}
}

// The test binary standing in for openvpn: it only waits to be stopped.
func TestHelperStray(t *testing.T) {
	if os.Getenv("OVPN_STRAY_CHILD") != "1" {
		t.Skip("run as a stand-in server only")
	}
	time.Sleep(time.Minute)
}

func startStandIn(t *testing.T, link, config string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(link, "-test.run=^TestHelperStray$", "standin", "--config", config)
	cmd.Env = append(os.Environ(), "OVPN_STRAY_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

// A lost server of this inbound is stopped; a server of another one is not.
func TestALostServerIsStoppedSoTheInboundCanStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "openvpn")
	if err := os.Symlink(os.Args[0], link); err != nil {
		t.Fatal(err)
	}
	saved := runtimeDirOverride
	runtimeDirOverride = dir
	t.Cleanup(func() { runtimeDirOverride = saved })
	lost := startStandIn(t, link, configPathForID(3))
	other := startStandIn(t, link, configPathForID(4))
	time.Sleep(300 * time.Millisecond)

	if left := stopStrays(3); left != 0 {
		t.Errorf("%d lost servers left running", left)
	}

	exited := make(chan struct{})
	go func() { _ = lost.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the lost server of this inbound is still running")
	}
	if gone(other.Process.Pid) {
		t.Error("the server of another inbound was stopped too")
	}
}
