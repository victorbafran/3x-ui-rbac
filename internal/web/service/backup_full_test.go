package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/fullbackup"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var realBackupSets = backupSets

// boxAt points every backup set at its own folder under root, as one machine
// has them, keeping what each set carries.
func boxAt(root string) func() []fullbackup.Set {
	return func() []fullbackup.Set {
		var out []fullbackup.Set
		for _, s := range realBackupSets() {
			dir := filepath.Join(root, s.Kind)
			s.Dir = func() string { return dir }
			out = append(out, s)
		}
		return out
	}
}

func setupFullBackup(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub xray binary is a shell script")
	}
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbtest.InitDB(t, filepath.Join(dbDir, "x-ui.db"))
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	t.Setenv("XUI_LOG_FOLDER", t.TempDir())
	if err := os.WriteFile(filepath.Join(bin, xray.GetBinaryName()), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backupSets = realBackupSets })
}

func writeBoxFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openUpload(t *testing.T, body []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func inboundRemark(t *testing.T) string {
	t.Helper()
	var ib model.Inbound
	if err := database.GetDB().First(&ib, 1).Error; err != nil {
		t.Fatal(err)
	}
	return ib.Remark
}

// The panel moved to a fresh server from a full backup: the database and the
// keys and certificates it points at are all there again.
func TestAFullBackupBringsTheFilesBackWithTheDatabase(t *testing.T) {
	setupFullBackup(t)
	old := t.TempDir()
	writeBoxFile(t, old, "relay-keys/relay-8", "front ssh key")
	writeBoxFile(t, old, "openvpn/ca-3.pem", "the ca every .ovpn embeds")
	writeBoxFile(t, old, "openvpn/openvpn-3.conf", "rebuilt at start")
	writeBoxFile(t, old, "certs/tun.example.ir/privkey.pem", "tls key")
	backupSets = boxAt(old)
	if err := database.GetDB().Create(&model.Inbound{
		Id: 1, Remark: "before", Port: 4430, Protocol: model.VLESS, Tag: "in-4430",
		Settings: "{}", StreamSettings: "{}", Sniffing: "{}",
	}).Error; err != nil {
		t.Fatal(err)
	}

	s := &ServerService{}
	archive, err := s.GetFullBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", 1).Update("remark", "after").Error; err != nil {
		t.Fatal(err)
	}

	fresh := t.TempDir()
	backupSets = boxAt(fresh)
	if err := s.ImportDB(openUpload(t, archive), true); err != nil {
		t.Fatalf("restoring the full backup: %v", err)
	}
	for rel, want := range map[string]string{
		"relay-keys/relay-8":               "front ssh key",
		"openvpn/ca-3.pem":                 "the ca every .ovpn embeds",
		"certs/tun.example.ir/privkey.pem": "tls key",
	} {
		got, err := os.ReadFile(filepath.Join(fresh, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s on the fresh box: %q, %v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fresh, "openvpn", "openvpn-3.conf")); err == nil {
		t.Error("a runtime file travelled in the backup")
	}
	if got := inboundRemark(t); got != "before" {
		t.Errorf("the database was not restored: remark %q", got)
	}
}

// A full backup whose database will not import leaves the box's own files as
// they were, rather than half of one backup and half of another.
func TestAFailedFullRestoreLeavesTheFilesAlone(t *testing.T) {
	setupFullBackup(t)
	src := t.TempDir()
	writeBoxFile(t, src, "relay-keys/relay-8", "the backup's key")
	files, err := fullbackup.Collect(boxAt(src)())
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "broken-full.tar.gz")
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fullbackup.Write(out, "x-ui.db", []byte("not a database at all"), files, time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	out.Close()
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	here := t.TempDir()
	writeBoxFile(t, here, "relay-keys/relay-8", "this box's key")
	backupSets = boxAt(here)
	if err := (&ServerService{}).ImportDB(openUpload(t, body), true); err == nil {
		t.Fatal("a backup with a broken database was imported")
	}
	if got, _ := os.ReadFile(filepath.Join(here, "relay-keys", "relay-8")); string(got) != "this box's key" {
		t.Errorf("the failed restore left relay-8 as %q", got)
	}
}
