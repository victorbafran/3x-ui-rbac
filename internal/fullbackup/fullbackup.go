// Package fullbackup builds and reads the panel's full backup: the database plus
// the files nothing can rebuild from it, and puts those files back on restore.
package fullbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Format and Version name what Write produces; Read refuses anything else.
const (
	Format      = "x-ui-full-backup"
	Version     = 1
	maxEntries  = 10000
	maxFileSize = 32 << 20
	maxDBSize   = 2 << 30
	// maxTrailer is what may follow the tar's end before the gzip checksum.
	maxTrailer = 1 << 20
)

// maxFilesTotal caps what a restore holds in memory; a variable for tests.
var maxFilesTotal int64 = 256 << 20

// Set is one folder a full backup carries, under its kind's name.
type Set struct {
	Kind string
	Dir  func() string
	// Keep picks the files that belong; nil keeps every regular file below Dir.
	Keep func(rel string) bool
	// Flat sets read only Dir's own files, not its subfolders.
	Flat bool
	// Private files are written 0600 whatever the archive says.
	Private bool
	// DirMode is what a folder made on restore gets; zero means 0755.
	DirMode fs.FileMode
}

// File is one file in a full backup; its bytes travel beside the manifest.
type File struct {
	Kind   string      `json:"kind"`
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`
	Size   int64       `json:"size"`
	SHA256 string      `json:"sha256"`
	data   []byte
}

// Manifest is the archive's record of what it holds.
type Manifest struct {
	Format   string    `json:"format"`
	Version  int       `json:"version"`
	Created  time.Time `json:"created"`
	Panel    string    `json:"panel"`
	Database string    `json:"database"`
	// DatabaseSHA256 lets Read refuse a database changed after the backup.
	DatabaseSHA256 string `json:"databaseSha256"`
	Files          []File `json:"files"`
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Collect reads every set's files from this box. A set whose folder
// does not exist has nothing to carry.
func Collect(sets []Set) ([]File, error) {
	var out []File
	for _, set := range sets {
		root := set.Dir()
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() {
				if set.Flat && p != root {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if set.Keep != nil && !set.Keep(rel) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > maxFileSize {
				return fmt.Errorf("%s is %d bytes, more than a backup carries", p, info.Size())
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out = append(out, File{
				Kind: set.Kind, Path: rel, Mode: info.Mode().Perm(),
				Size: int64(len(data)), SHA256: digest(data), data: data,
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s for the backup: %w", set.Kind, err)
		}
	}
	return out, nil
}

// Write writes the manifest, the database and the files as a tar.gz.
func Write(w io.Writer, dbName string, db []byte, files []File, now time.Time, panel string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	manifest, err := json.MarshalIndent(Manifest{
		Format: Format, Version: Version, Created: now.UTC(),
		Panel: panel, Database: dbName, DatabaseSHA256: digest(db), Files: files,
	}, "", "  ")
	if err != nil {
		return err
	}
	put := func(name string, mode fs.FileMode, body []byte) error {
		hdr := &tar.Header{Name: name, Mode: int64(mode), Size: int64(len(body)), ModTime: now, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := put("manifest.json", 0o600, manifest); err != nil {
		return err
	}
	if err := put(dbName, 0o600, db); err != nil {
		return err
	}
	for _, f := range files {
		if err := put(path.Join("files", f.Kind, f.Path), f.Mode, f.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// cleanRel refuses a path that is absolute or climbs out of its folder.
func cleanRel(rel string) (string, error) {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") ||
		path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q is not a path inside the backup's folders", rel)
	}
	return rel, nil
}

// Read reads a tar.gz from Write, streaming its database to dbPath and checking
// every entry against the manifest; anything else in it is refused, not trusted.
func Read(r io.Reader, dbPath string) (Manifest, []File, error) {
	m, files, err := read(r, dbPath)
	if err != nil {
		_ = os.Remove(dbPath)
	}
	return m, files, err
}

func read(r io.Reader, dbPath string) (Manifest, []File, error) {
	var m Manifest
	gz, err := gzip.NewReader(r)
	if err != nil {
		return m, nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	bodies := map[string][]byte{}
	var want map[string]int64
	dbSum := ""
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, fmt.Errorf("the archive is damaged: %w", err)
		}
		if n >= maxEntries {
			return m, nil, errors.New("the archive holds more entries than a backup has")
		}
		if hdr.Typeflag != tar.TypeReg {
			return m, nil, fmt.Errorf("%q is not a plain file; a backup holds only plain files", hdr.Name)
		}
		if _, err := cleanRel(hdr.Name); err != nil {
			return m, nil, err
		}
		switch {
		case n == 0:
			if hdr.Name != "manifest.json" || hdr.Size > maxFileSize {
				return m, nil, errors.New("this archive is not a full backup of this panel")
			}
			if m, err = readManifest(tr); err != nil {
				return m, nil, err
			}
			if want, err = recorded(m); err != nil {
				return m, nil, err
			}
		case hdr.Name == m.Database && dbSum == "":
			if hdr.Size > maxDBSize {
				return m, nil, fmt.Errorf("the database is %d bytes, more than a backup holds", hdr.Size)
			}
			if dbSum, err = stream(tr, dbPath); err != nil {
				return m, nil, fmt.Errorf("unpack the database: %w", err)
			}
		case strings.HasPrefix(hdr.Name, "files/"):
			size, listed := want[hdr.Name]
			if _, seen := bodies[hdr.Name]; !listed || seen || hdr.Size != size {
				return m, nil, fmt.Errorf("%q is not in the backup's record of its files", hdr.Name)
			}
			body, err := io.ReadAll(io.LimitReader(tr, size+1))
			if err != nil {
				return m, nil, fmt.Errorf("read %q: %w", hdr.Name, err)
			}
			bodies[hdr.Name] = body
		default:
			return m, nil, fmt.Errorf("%q is not part of a full backup", hdr.Name)
		}
	}
	// The gzip checksum is only checked once the stream is read to its end.
	if n, err := io.Copy(io.Discard, io.LimitReader(gz, maxTrailer+1)); err != nil {
		return m, nil, fmt.Errorf("the archive is damaged: %w", err)
	} else if n > maxTrailer {
		return m, nil, errors.New("the archive carries data after its end")
	}
	if m.Format == "" {
		return m, nil, errors.New("this archive is not a full backup of this panel")
	}
	if dbSum == "" {
		return m, nil, errors.New("the backup holds no database")
	}
	if dbSum != m.DatabaseSHA256 {
		return m, nil, errors.New("the database does not match the backup's record of it")
	}
	files := make([]File, 0, len(m.Files))
	for _, f := range m.Files {
		if _, err := cleanRel(f.Path); err != nil {
			return m, nil, err
		}
		body, ok := bodies[path.Join("files", f.Kind, f.Path)]
		if !ok || digest(body) != f.SHA256 {
			return m, nil, fmt.Errorf("%s/%s is missing or does not match the backup's record of it", f.Kind, f.Path)
		}
		f.data = body
		files = append(files, f)
	}
	return m, files, nil
}

func readManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	body, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return m, fmt.Errorf("read the manifest: %w", err)
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Format != Format {
		return Manifest{}, errors.New("this archive is not a full backup of this panel")
	}
	if m.Version > Version {
		return Manifest{}, fmt.Errorf("this backup was made by a newer panel (format %d); update this one first", m.Version)
	}
	if m.Database != "x-ui.db" && m.Database != "x-ui.dump" {
		return Manifest{}, fmt.Errorf("the backup names its database %q", m.Database)
	}
	return m, nil
}

// recorded is every file entry the manifest lists, with its size; nothing
// else is read, and the total is capped before any of it is.
func recorded(m Manifest) (map[string]int64, error) {
	want := make(map[string]int64, len(m.Files))
	var total int64
	for _, f := range m.Files {
		if _, err := cleanRel(f.Path); err != nil {
			return nil, err
		}
		if f.Size < 0 || f.Size > maxFileSize {
			return nil, fmt.Errorf("%s/%s is %d bytes, more than a backup holds", f.Kind, f.Path, f.Size)
		}
		total += f.Size
		want[path.Join("files", f.Kind, f.Path)] = f.Size
	}
	if total > maxFilesTotal {
		return nil, fmt.Errorf("the backup's files come to %d bytes, more than a restore holds", total)
	}
	return want, nil
}

// stream copies the database entry to path and returns its digest.
func stream(r io.Reader, path string) (string, error) {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, maxDBSize+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDBSize {
		err = errors.New("the database is larger than a backup holds")
	}
	return hex.EncodeToString(h.Sum(nil)), err
}

// Restore puts the files back where this box keeps them. A file it
// replaces is copied under keepDir first; undo puts everything back as it was.
func Restore(sets []Set, files []File, keepDir string) (func(), error) {
	byKind := map[string]Set{}
	for _, s := range sets {
		byKind[s.Kind] = s
	}
	var undos []func()
	undo := func() {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
	}
	for _, f := range files {
		set, ok := byKind[f.Kind]
		if !ok || (set.Keep != nil && !set.Keep(f.Path)) || (set.Flat && strings.Contains(f.Path, "/")) {
			// A kind or a file this panel does not keep: written by a newer one.
			logger.Warningf("restore: skipping %s/%s, which this panel does not keep", f.Kind, f.Path)
			continue
		}
		target := filepath.Join(set.Dir(), filepath.FromSlash(f.Path))
		var undoFile func()
		old, err := os.ReadFile(target)
		switch {
		case err == nil && bytes.Equal(old, f.data):
			continue
		case err == nil:
			kept := filepath.Join(keepDir, f.Kind, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
				return undo, err
			}
			if err := os.WriteFile(kept, old, 0o600); err != nil {
				return undo, err
			}
			undoFile = func() { _ = writeAtomic(target, old, 0o600) }
		case errors.Is(err, fs.ErrNotExist):
			undoFile = func() { _ = os.Remove(target) }
		default:
			return undo, err
		}
		mode := f.Mode & 0o644
		if set.Private || mode == 0 {
			mode = 0o600
		}
		dirMode := set.DirMode
		if dirMode == 0 {
			dirMode = 0o755
		}
		made, err := mkdirs(filepath.Dir(target), dirMode)
		for _, d := range made {
			undos = append(undos, func() { _ = os.Remove(d) })
		}
		if err != nil {
			return undo, err
		}
		// After its folders, so undo takes the file away before them.
		undos = append(undos, undoFile)
		if err := writeAtomic(target, f.data, mode); err != nil {
			return undo, fmt.Errorf("restore %s/%s: %w", f.Kind, f.Path, err)
		}
	}
	return undo, nil
}

// mkdirs makes dir and every missing parent with mode exactly, whatever the
// umask, and returns the folders it made, outermost first.
func mkdirs(dir string, mode fs.FileMode) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Stat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	var made []string
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], mode); err != nil {
			return made, err
		}
		made = append(made, missing[i])
		if err := os.Chmod(missing[i], mode); err != nil {
			return made, err
		}
	}
	return made, nil
}

// writeAtomic writes through a temporary file beside path, so a reader sees
// the old file or the new one and never half of either.
func writeAtomic(path string, body []byte, mode fs.FileMode) error {
	tmp := path + ".restore-tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// IsArchive reports whether a file starts like the gzip stream Write produces.
func IsArchive(head []byte) bool {
	return len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b
}
