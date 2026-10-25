package openvpn

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// shortDir keeps a unix socket path under the 108 bytes the kernel allows.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ov")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	saved := runtimeDirOverride
	runtimeDirOverride = dir
	t.Cleanup(func() { runtimeDirOverride = saved })
	return dir
}

// fakeManagement answers one command on an inbound's socket the way openvpn
// does, greeting first, and hands back the line it was sent.
func fakeManagement(t *testing.T, id int, reply string) <-chan string {
	t.Helper()
	ln, err := net.Listen("unix", managementSocketForID(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\r\n"))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- strings.TrimSpace(line)
		_, _ = conn.Write([]byte(reply))
	}()
	return got
}

// openvpn runs as nobody, out of reach of a panel without CAP_KILL, but its
// own socket still takes the order to shut down.
func TestOpenVPNIsAskedToExitThroughItsSocket(t *testing.T) {
	shortDir(t)
	sent := fakeManagement(t, 7, "SUCCESS: signal SIGTERM thrown\r\n")
	if err := AskToExit(7); err != nil {
		t.Fatalf("asking openvpn to exit: %v", err)
	}
	if got := <-sent; got != "signal SIGTERM" {
		t.Errorf("sent %q, want the management command signal SIGTERM", got)
	}
}

func TestARefusedExitIsAnError(t *testing.T) {
	shortDir(t)
	fakeManagement(t, 7, "ERROR: signal 'SIGTERM' is currently ignored\r\n")
	if err := AskToExit(7); err == nil {
		t.Error("an openvpn that refused to exit was taken as gone")
	}
}

func TestNoSocketMeansNoExit(t *testing.T) {
	shortDir(t)
	if err := AskToExit(7); err == nil {
		t.Error("an inbound with no socket was taken as asked")
	}
}

// The test binary standing in for an openvpn that leaves when its socket
// says so, and writes down that it left that way.
func TestHelperManagedServer(t *testing.T) {
	if os.Getenv("OVPN_MANAGED_CHILD") != "1" {
		t.Skip("run as a stand-in server only")
	}
	id, _ := strconv.Atoi(os.Getenv("OVPN_MANAGED_ID"))
	ln, err := net.Listen("unix", managementSocketForID(id))
	if err != nil {
		os.Exit(3)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(4)
		}
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if strings.TrimSpace(line) == "signal SIGTERM" {
			_, _ = conn.Write([]byte("SUCCESS: signal SIGTERM thrown\r\n"))
			_ = os.WriteFile(os.Getenv("OVPN_MANAGED_MARK"), []byte("socket"), 0o600)
			os.Exit(0)
		}
		conn.Close()
	}
}

func startHelper(t *testing.T, name string) *sidecar.Process {
	t.Helper()
	proc := sidecar.New(os.Args[0], "test", "-test.run=^"+name+"$")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Stop() })
	return proc
}

// Stopping an inbound goes through the socket, so the server leaves on its
// own instead of being signalled, which the kernel would refuse.
func TestStopAsksTheServerThroughItsSocket(t *testing.T) {
	dir := shortDir(t)
	mark := filepath.Join(dir, "left")
	t.Setenv("XUI_OPENVPN_DIR", dir)
	t.Setenv("OVPN_MANAGED_CHILD", "1")
	t.Setenv("OVPN_MANAGED_ID", "7")
	t.Setenv("OVPN_MANAGED_MARK", mark)
	proc := startHelper(t, "TestHelperManagedServer")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(managementSocketForID(7)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in server never opened its socket")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := stopServer(7, proc); err != nil {
		t.Fatalf("stopping the server: %v", err)
	}
	if proc.IsRunning() {
		t.Fatal("the server is still running")
	}
	if got, _ := os.ReadFile(mark); string(got) != "socket" {
		t.Error("the server was not asked through its socket")
	}
}

// A server that takes the order and stays is still stopped the old way.
func TestAServerThatStaysAfterTheAskIsStopped(t *testing.T) {
	t.Setenv("OVPN_STRAY_CHILD", "1")
	saved, savedGrace := askToExit, stopGrace
	askToExit = func(int) error { return nil }
	stopGrace = 200 * time.Millisecond
	t.Cleanup(func() { askToExit, stopGrace = saved, savedGrace })
	proc := startHelper(t, "TestHelperStray")

	if err := stopServer(7, proc); err != nil {
		t.Fatalf("stopping the server: %v", err)
	}
	if proc.IsRunning() {
		t.Error("a server that ignored the ask was left running")
	}
}

// A lost server out of the panel's reach keeps its socket and its device: a
// new one would only fail busy, and the old one goes on serving.
func TestALostServerThatWillNotStopBlocksTheStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strays are found in /proc")
	}
	shortDir(t)
	root := fakeProc(t)
	writeProc(t, root, "100", "cmdline", "/usr/sbin/openvpn\x00--config\x00"+configPathForID(3)+"\x00")
	writeProc(t, root, "100", "stat", "100 (openvpn) S 1 100 100 0 -1")
	savedAsk, savedSignal, savedGrace := askToExit, signalPid, stopGrace
	askToExit = func(int) error { return errors.New("no socket") }
	signalPid = func(int, syscall.Signal) error { return syscall.EPERM }
	stopGrace = 100 * time.Millisecond
	t.Cleanup(func() { askToExit, signalPid, stopGrace = savedAsk, savedSignal, savedGrace })
	if err := os.WriteFile(managementSocketForID(3), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := clearForStart(3); err == nil {
		t.Error("a new server was cleared to start beside one still running")
	}
	if _, err := os.Stat(managementSocketForID(3)); err != nil {
		t.Error("the running server's socket was deleted")
	}
}

// A lost server that does stop leaves the way clear, socket and all.
func TestALostServerThatStopsClearsTheStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strays are found in /proc")
	}
	shortDir(t)
	root := fakeProc(t)
	writeProc(t, root, "100", "cmdline", "/usr/sbin/openvpn\x00--config\x00"+configPathForID(3)+"\x00")
	writeProc(t, root, "100", "stat", "100 (openvpn) S 1 100 100 0 -1")
	savedAsk, savedSignal, savedGrace := askToExit, signalPid, stopGrace
	askToExit = func(int) error {
		writeProc(t, root, "100", "stat", "100 (openvpn) Z 1 100 100 0 -1")
		return nil
	}
	signalPid = func(int, syscall.Signal) error { return syscall.EPERM }
	stopGrace = time.Second
	t.Cleanup(func() { askToExit, signalPid, stopGrace = savedAsk, savedSignal, savedGrace })
	if err := os.WriteFile(managementSocketForID(3), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := clearForStart(3); err != nil {
		t.Fatalf("a stopped stray still blocked the start: %v", err)
	}
	if _, err := os.Stat(managementSocketForID(3)); err == nil {
		t.Error("the dead server's socket was left to block the new bind")
	}
}
