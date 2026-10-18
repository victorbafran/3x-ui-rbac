//go:build !windows

package ocserv

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The panel's systemd unit sets UMask=0077, so MkdirAll creates the parent at
// 0700 and an explicit chmod on the leaf alone leaves the worker outside a
// wall it cannot see: the socket has the right owner and mode, and the worker
// still gets "Permission denied". Every directory created on the way has to
// be traversable.
func TestEveryDirectoryCreatedOnTheWayIsTraversable(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	base := openTempBase(t)
	runtimeDirOverride = filepath.Join(base, "x-ui", "ocserv")
	t.Cleanup(func() { runtimeDirOverride = "" })

	if err := ensureRuntimeDir(); err != nil {
		t.Fatalf("ensureRuntimeDir: %v", err)
	}
	for _, dir := range []string{
		filepath.Join(base, "x-ui"),
		filepath.Join(base, "x-ui", "ocserv"),
	} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if st.Mode().Perm()&0o001 == 0 {
			t.Errorf("%s is mode %04o: the unprivileged worker cannot enter it", dir, st.Mode().Perm())
		}
	}
}

// Only what it creates. A directory the operator already placed is theirs,
// and quietly widening it would be a surprise in the wrong direction.
func TestAnExistingDirectoryIsNotWidened(t *testing.T) {
	base := openTempBase(t)
	mine := filepath.Join(base, "private")
	if err := os.Mkdir(mine, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runtimeDirOverride = filepath.Join(mine, "ocserv")
	t.Cleanup(func() { runtimeDirOverride = "" })

	// It reports rather than fixes, so the error is expected here.
	_ = ensureRuntimeDir()

	st, err := os.Stat(mine)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("the operator's own directory was changed to %04o", st.Mode().Perm())
	}
}

// A wall it will not climb, it names — otherwise the operator sees a socket
// with correct permissions and no reason for the failure.
func TestABlockedParentIsNamedWithTheFix(t *testing.T) {
	base := openTempBase(t)
	blocked := filepath.Join(base, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(blocked, "ocserv")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := traversabilityError(target)
	if err == nil {
		t.Fatal("a parent the worker cannot enter was not reported")
	}
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("the message does not name the directory in the way: %v", err)
	}
	if !strings.Contains(err.Error(), "chmod o+x") {
		t.Errorf("the message does not say how to fix it: %v", err)
	}
}

func TestATraversablePathIsNotReported(t *testing.T) {
	base := openTempBase(t)
	target := filepath.Join(base, "ocserv")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := traversabilityError(target); err != nil {
		t.Fatalf("a perfectly reachable path was reported: %v", err)
	}
}
