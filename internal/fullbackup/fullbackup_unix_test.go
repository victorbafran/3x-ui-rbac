//go:build !windows

package fullbackup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A folder a restore makes gets its own mode, not what the umask leaves:
// openvpn drops to nobody and must still reach its keys.
func TestFoldersAreMadeWithTheirOwnModeWhateverTheUmask(t *testing.T) {
	old, fresh := newBox(t), newBox(t)
	fill(t, old)
	_, _, files, err := unpack(t, archive(t, old, "db"))
	if err != nil {
		t.Fatal(err)
	}
	saved := syscall.Umask(0o077)
	defer syscall.Umask(saved)
	if _, err := Restore(fresh.sets(), files, filepath.Join(t.TempDir(), "kept")); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]os.FileMode{
		fresh.openvpn:               0o755,
		filepath.Dir(fresh.openvpn): 0o755,
		fresh.ocserv:                0o755,
		fresh.relays:                0o700,
		filepath.Join(fresh.certs, "tun.example.ir"): 0o700,
	} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s came back as %v, %v; want %v", dir, info.Mode().Perm(), err, want)
		}
	}
}
