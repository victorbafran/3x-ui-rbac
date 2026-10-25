package fullbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	openvpnKeys = regexp.MustCompile(`^(ca-\d+\.pem|ca-\d+\.pem\.key|cert-\d+\.pem|key-\d+\.pem|tls-crypt-\d+\.key)$`)
	ocservKeys  = regexp.MustCompile(`^(cert|key)-\d+\.pem$`)
)

// box is one machine's folders, laid out the way the panel keeps them.
type box struct{ certs, relays, openvpn, ocserv, token string }

func newBox(t *testing.T) box {
	t.Helper()
	root := t.TempDir()
	b := box{
		certs:   filepath.Join(root, "etc", "certs"),
		relays:  filepath.Join(root, "etc", "relay-keys"),
		openvpn: filepath.Join(root, "var", "openvpn"),
		ocserv:  filepath.Join(root, "var", "ocserv"),
		token:   filepath.Join(root, "etc"),
	}
	return b
}

func (b box) sets() []Set {
	notTemp := func(rel string) bool { return !strings.HasSuffix(rel, ".tmp") }
	return []Set{
		{Kind: "certs", Dir: func() string { return b.certs }, Keep: notTemp, DirMode: 0o700},
		{Kind: "relay-keys", Dir: func() string { return b.relays }, Keep: notTemp, Flat: true, Private: true, DirMode: 0o700},
		{Kind: "openvpn", Dir: func() string { return b.openvpn }, Keep: openvpnKeys.MatchString, Flat: true},
		{Kind: "ocserv", Dir: func() string { return b.ocserv }, Keep: ocservKeys.MatchString, Flat: true},
		{
			Kind: "node-token", Dir: func() string { return b.token },
			Keep: func(rel string) bool { return rel == "node_token_key.json" }, Flat: true, Private: true, DirMode: 0o700,
		},
	}
}

func put(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// fill lays out what the live German box holds, runtime files included.
func fill(t *testing.T, b box) {
	t.Helper()
	put(t, filepath.Join(b.certs, "tun.example.ir", "fullchain.pem"), "chain", 0o644)
	put(t, filepath.Join(b.certs, "tun.example.ir", "privkey.pem"), "tun key", 0o600)
	put(t, filepath.Join(b.certs, "tun.example.ir", "meta.json"), "{}", 0o600)
	put(t, filepath.Join(b.certs, "tun.example.ir", "fullchain.pem.tmp"), "half", 0o600)
	put(t, filepath.Join(b.certs, "account.key"), "acme", 0o600)
	put(t, filepath.Join(b.relays, "relay-8"), "front ssh key", 0o600)
	put(t, filepath.Join(b.relays, "relay-9"), "exit ssh key", 0o600)
	for _, name := range []string{"ca-3.pem", "ca-3.pem.key", "cert-3.pem", "key-3.pem", "tls-crypt-3.key"} {
		put(t, filepath.Join(b.openvpn, name), "ovpn "+name, 0o600)
	}
	for _, name := range []string{"openvpn-3.conf", "status-3.log", "ovpasswd-3"} {
		put(t, filepath.Join(b.openvpn, name), "runtime", 0o600)
	}
	put(t, filepath.Join(b.ocserv, "cert-1.pem"), "cisco cert", 0o644)
	put(t, filepath.Join(b.ocserv, "key-1.pem"), "cisco key", 0o600)
	put(t, filepath.Join(b.ocserv, "ocserv-1.conf"), "runtime", 0o600)
	put(t, filepath.Join(b.ocserv, "peruser-1", "ali"), "runtime", 0o600)
	put(t, filepath.Join(b.token, "node_token_key.json"), `{"k":1}`, 0o600)
	put(t, filepath.Join(b.token, "system_metrics.gob"), "history", 0o600)
}

// unpack reads an archive the way a restore does, the database landing on disk.
func unpack(t *testing.T, data []byte) (Manifest, string, []File, error) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "x-ui.db.archive")
	m, files, err := Read(bytes.NewReader(data), dbPath)
	db, _ := os.ReadFile(dbPath)
	return m, string(db), files, err
}

func names(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Kind+"/"+f.Path)
	}
	slices.Sort(out)
	return out
}

// The backup carries what nothing rebuilds and leaves out what the panel
// writes again from the database at every start.
func TestOnlyTheFilesNothingRebuildsAreCarried(t *testing.T) {
	b := newBox(t)
	fill(t, b)
	files, err := Collect(b.sets())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"certs/account.key", "certs/tun.example.ir/fullchain.pem", "certs/tun.example.ir/meta.json",
		"certs/tun.example.ir/privkey.pem", "node-token/node_token_key.json",
		"ocserv/cert-1.pem", "ocserv/key-1.pem",
		"openvpn/ca-3.pem", "openvpn/ca-3.pem.key", "openvpn/cert-3.pem", "openvpn/key-3.pem", "openvpn/tls-crypt-3.key",
		"relay-keys/relay-8", "relay-keys/relay-9",
	}
	if got := names(files); !slices.Equal(got, want) {
		t.Errorf("carried %v\nwant %v", got, want)
	}
}

// A folder that does not exist on this box has nothing to carry.
func TestAMissingFolderCarriesNothing(t *testing.T) {
	b := newBox(t)
	files, err := Collect(b.sets())
	if err != nil || len(files) != 0 {
		t.Fatalf("files %v, err %v; an empty box has nothing to back up", names(files), err)
	}
}

func archive(t *testing.T, b box, db string) []byte {
	t.Helper()
	files, err := Collect(b.sets())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, "x-ui.db", []byte(db), files, time.Now(), "v3.10.2-rbac35"); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The panel moved to a fresh server: every file lands where that server keeps
// it, with its content, and the keys stay private.
func TestARestoreOnAFreshBoxPutsEveryFileBack(t *testing.T) {
	old, fresh := newBox(t), newBox(t)
	fill(t, old)
	m, db, files, err := unpack(t, archive(t, old, "the database"))
	if err != nil {
		t.Fatal(err)
	}
	if db != "the database" || m.Database != "x-ui.db" || m.Panel != "v3.10.2-rbac35" {
		t.Fatalf("read back db %q, manifest %+v", db, m)
	}
	if _, err := Restore(fresh.sets(), files, filepath.Join(t.TempDir(), "kept")); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		set := map[string]string{"certs": fresh.certs, "relay-keys": fresh.relays, "openvpn": fresh.openvpn, "ocserv": fresh.ocserv, "node-token": fresh.token}[f.Kind]
		got, err := os.ReadFile(filepath.Join(set, filepath.FromSlash(f.Path)))
		if err != nil || string(got) != string(f.data) {
			t.Errorf("%s/%s on the fresh box: %q, %v", f.Kind, f.Path, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(fresh.relays, "relay-8"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the relay key came back as %v, %v; want 0600", info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fresh.openvpn, "openvpn-3.conf")); err == nil {
		t.Error("a runtime file was restored")
	}
}

// Restoring over a box that has its own files keeps a copy of each one it
// replaces, skips what is already the same, and can be undone.
func TestARestoreKeepsWhatItReplacesAndCanBeUndone(t *testing.T) {
	old, here := newBox(t), newBox(t)
	fill(t, old)
	put(t, filepath.Join(here.relays, "relay-8"), "a newer key", 0o600)
	put(t, filepath.Join(here.openvpn, "ca-3.pem"), "ovpn ca-3.pem", 0o600)
	_, _, files, err := unpack(t, archive(t, old, "db"))
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(t.TempDir(), "kept")
	undo, err := Restore(here.sets(), files, kept)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(here.relays, "relay-8")); string(got) != "front ssh key" {
		t.Errorf("relay-8 is %q after the restore", got)
	}
	if got, _ := os.ReadFile(filepath.Join(kept, "relay-keys", "relay-8")); string(got) != "a newer key" {
		t.Errorf("the key the restore replaced was not kept: %q", got)
	}
	if _, err := os.Stat(filepath.Join(kept, "openvpn", "ca-3.pem")); err == nil {
		t.Error("a file already the same was copied aside as if replaced")
	}

	undo()
	if got, _ := os.ReadFile(filepath.Join(here.relays, "relay-8")); string(got) != "a newer key" {
		t.Errorf("undo left relay-8 as %q", got)
	}
	if _, err := os.Stat(filepath.Join(here.relays, "relay-9")); err == nil {
		t.Error("undo left a file the restore had added")
	}
}

type entry struct {
	name string
	body string
	kind byte
}

func craft(t *testing.T, manifest *Manifest, entries ...entry) []byte {
	t.Helper()
	return craftWithTrailer(t, 0, manifest, entries...)
}

// craftWithTrailer writes trailer zero bytes after the tar's end, inside the
// gzip stream, where a reader that stops at the tar would never look.
func craftWithTrailer(t *testing.T, trailer int, manifest *Manifest, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if manifest != nil {
		body, _ := json.Marshal(manifest)
		entries = append([]entry{{name: "manifest.json", body: string(body)}}, entries...)
	}
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: kind}
		if kind == tar.TypeSymlink {
			hdr.Size, hdr.Linkname = 0, "/etc/shadow"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = tw.Close()
	if trailer > 0 {
		if _, err := gz.Write(make([]byte, trailer)); err != nil {
			t.Fatal(err)
		}
	}
	_ = gz.Close()
	return buf.Bytes()
}

// A crafted archive cannot write outside the folders a backup restores into,
// smuggle in a link, or pass a changed file off as the one recorded.
func TestAnArchiveCannotReachOutsideItsFolders(t *testing.T) {
	sum := digest([]byte("db"))
	good := &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: sum}
	db := entry{name: "x-ui.db", body: "db"}
	if _, _, _, err := unpack(t, craft(t, good, db)); err != nil {
		t.Fatalf("the well-formed archive the cases spoil was refused: %v", err)
	}
	cases := map[string][]byte{
		"climbing entry":    craft(t, good, db, entry{name: "files/certs/../../evil", body: "x"}),
		"absolute entry":    craft(t, good, db, entry{name: "/etc/passwd", body: "x"}),
		"symlink entry":     craft(t, good, db, entry{name: "files/certs/link", kind: tar.TypeSymlink}),
		"stranger entry":    craft(t, good, db, entry{name: "x-ui.db.extra", body: "x"}),
		"climbing record":   craft(t, &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: sum, Files: []File{{Kind: "certs", Path: "../evil", SHA256: digest([]byte("x"))}}}, db),
		"changed file":      craft(t, &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: sum, Files: []File{{Kind: "certs", Path: "a.pem", Size: 7, SHA256: digest([]byte("recorded"))}}}, db, entry{name: "files/certs/a.pem", body: "swapped"}),
		"unlisted file":     craft(t, good, db, entry{name: "files/certs/a.pem", body: ""}),
		"size that lies":    craft(t, &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: sum, Files: []File{{Kind: "certs", Path: "a.pem", Size: 3, SHA256: digest([]byte("abcd"))}}}, db, entry{name: "files/certs/a.pem", body: "abcd"}),
		"the folder itself": craft(t, &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: sum, Files: []File{{Kind: "relay-keys", Path: ".", Size: 1, SHA256: digest([]byte("x"))}}}, db, entry{name: "files/relay-keys", body: "x"}),
		"data after end":    craftWithTrailer(t, maxTrailer+1, good, db),
		"changed database":  craft(t, good, entry{name: "x-ui.db", body: "dX"}),
		"not a backup":      craft(t, nil, db),
		"manifest last":     craft(t, nil, db, entry{name: "manifest.json", body: `{"format":"x-ui-full-backup","version":1,"database":"x-ui.db"}`}),
		"newer format":      craft(t, &Manifest{Format: Format, Version: Version + 1, Database: "x-ui.db", DatabaseSHA256: sum}, db),
		"no database":       craft(t, good),
	}
	for name, data := range cases {
		dbPath := filepath.Join(t.TempDir(), "x-ui.db.archive")
		if _, _, err := Read(bytes.NewReader(data), dbPath); err == nil {
			t.Errorf("%s: the archive was accepted", name)
		}
		if _, err := os.Stat(dbPath); err == nil {
			t.Errorf("%s: a refused archive left its database behind", name)
		}
	}
}

// Files are held in memory until the restore, so their total is capped.
func TestABackupTooBigToHoldIsRefused(t *testing.T) {
	saved := maxFilesTotal
	maxFilesTotal = 6
	t.Cleanup(func() { maxFilesTotal = saved })
	files := []File{
		{Kind: "certs", Path: "a.pem", Size: 4, SHA256: digest([]byte("aaaa"))},
		{Kind: "certs", Path: "b.pem", Size: 4, SHA256: digest([]byte("bbbb"))},
	}
	m := &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: digest([]byte("db")), Files: files}
	data := craft(t, m, entry{name: "x-ui.db", body: "db"}, entry{name: "files/certs/a.pem", body: "aaaa"}, entry{name: "files/certs/b.pem", body: "bbbb"})
	if _, _, _, err := unpack(t, data); err == nil {
		t.Error("files over the cap were taken into memory")
	}
	maxFilesTotal = 8
	if _, _, _, err := unpack(t, data); err != nil {
		t.Errorf("files at the cap were refused: %v", err)
	}
}

// A little padding after the tar, as some tools write, is not a damaged archive.
func TestPaddingAfterTheTarIsAccepted(t *testing.T) {
	good := &Manifest{Format: Format, Version: Version, Database: "x-ui.db", DatabaseSHA256: digest([]byte("db"))}
	if _, _, _, err := unpack(t, craftWithTrailer(t, 10240, good, entry{name: "x-ui.db", body: "db"})); err != nil {
		t.Errorf("an archive padded to a tar record was refused: %v", err)
	}
}

// A backup damaged in transit, a byte flipped after the last file, is refused
// by gzip's own checksum rather than restored.
func TestADamagedArchiveIsRefused(t *testing.T) {
	b := newBox(t)
	fill(t, b)
	data := archive(t, b, "the database")
	data[len(data)-6] ^= 0xff
	if _, _, _, err := unpack(t, data); err == nil {
		t.Error("an archive with a broken checksum was accepted")
	}
}

// Undo also takes away the folders the restore had to make.
func TestUndoRemovesTheFoldersARestoreMade(t *testing.T) {
	old, fresh := newBox(t), newBox(t)
	fill(t, old)
	_, _, files, err := unpack(t, archive(t, old, "db"))
	if err != nil {
		t.Fatal(err)
	}
	undo, err := Restore(fresh.sets(), files, filepath.Join(t.TempDir(), "kept"))
	if err != nil {
		t.Fatal(err)
	}
	undo()
	for _, dir := range []string{fresh.relays, fresh.openvpn, filepath.Join(fresh.certs, "tun.example.ir"), filepath.Dir(fresh.openvpn)} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("undo left %s, which the restore had made", dir)
		}
	}
}

// A file the panel would never back up into a folder, or a folder it does not
// keep, is skipped rather than written.
func TestOnlyWhatABackupWouldCarryIsRestored(t *testing.T) {
	here := newBox(t)
	files := []File{
		{Kind: "openvpn", Path: "openvpn-3.conf", Mode: 0o600, data: []byte("injected config")},
		{Kind: "relay-keys", Path: "sub/relay-1", Mode: 0o600, data: []byte("nested")},
		{Kind: "elsewhere", Path: "x", Mode: 0o600, data: []byte("unknown kind")},
	}
	if _, err := Restore(here.sets(), files, filepath.Join(t.TempDir(), "kept")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(here.openvpn, "openvpn-3.conf"), filepath.Join(here.relays, "sub", "relay-1")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was written from the archive", p)
		}
	}
}

func TestAnArchiveIsToldFromADatabase(t *testing.T) {
	if !IsArchive([]byte{0x1f, 0x8b, 8}) || IsArchive([]byte("SQLite format 3")) || IsArchive(nil) {
		t.Error("IsArchive is wrong about what it was given")
	}
}
