package openvpn

// File ownership is a linux question — syscall.Stat_t does not exist
// elsewhere — and the panel's servers are linux. The _linux_test suffix is
// what keeps the package building on a developer's Windows machine; a
// runtime skip would not, because the file still has to compile.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The file holds bcrypt hashes. The group openvpn drops to has to read it;
// nobody else on the host does.
//
// This is the test that was missing. The credentials file was written
// root-only while openvpn was told to drop to nobody, so the helper openvpn
// spawns to check a password could not open it, and every customer was told
// their password was wrong.
func TestTheCredentialsFileIsReadableByThatGroupAndNoWider(t *testing.T) {
	dir := t.TempDir()
	runtimeDirOverride = dir
	t.Cleanup(func() { runtimeDirOverride = "" })

	inst := Instance{
		Id:    5,
		Port:  1194,
		Users: []User{{Name: "a@b", Login: "a@b", Password: "pw123456"}},
	}
	// This process's own group: chowning to any other needs root, and a test
	// that silently did nothing would be worse than no test.
	gid := os.Getgid()
	if err := writeUsers(inst, &DropAccount{User: "nobody", Group: "test", GID: gid}); err != nil {
		t.Fatalf("writing the credentials: %v", err)
	}

	path := filepath.Join(dir, "ovpasswd-5")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("mode is %o, want 640: the group must read it and nobody else should", mode)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("could not read the file's ownership")
	}
	if int(st.Gid) != gid {
		t.Errorf("group is %d, want %d — the helper runs as that group and could not open this file", st.Gid, gid)
	}
}

// A gid below zero means "leave the ownership alone", which is what the
// config and every other generated file wants.
func TestWritingWithoutAGroupLeavesOwnershipAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain")
	if err := writeFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("could not read the file's ownership")
	}
	if int(st.Gid) != os.Getgid() {
		t.Errorf("group is %d, want this process's own %d — writeFile changed an ownership it was not asked to", st.Gid, os.Getgid())
	}
}
