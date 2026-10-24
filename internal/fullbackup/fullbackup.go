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
)

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
	Files    []File    `json:"files"`
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
		Panel: panel, Database: dbName, Files: files,
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
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") ||
		path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q is not a path inside the backup's folders", rel)
	}
	return rel, nil
}

// Read reads a tar.gz from Write, checking every file
// against the manifest; anything else in it is refused, not trusted.
func Read(r io.Reader) (Manifest, []byte, []File, error) {
	var m Manifest
	gz, err := gzip.NewReader(r)
	if err != nil {
		return m, nil, nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	bodies := map[string][]byte{}
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, nil, fmt.Errorf("the archive is damaged: %w", err)
		}
		if n >= maxEntries {
			return m, nil, nil, errors.New("the archive holds more entries than a backup has")
		}
		if hdr.Typeflag != tar.TypeReg {
			return m, nil, nil, fmt.Errorf("%q is not a plain file; a backup holds only plain files", hdr.Name)
		}
		if _, err := cleanRel(hdr.Name); err != nil {
			return m, nil, nil, err
		}
		limit := int64(maxFileSize)
		if !strings.HasPrefix(hdr.Name, "files/") && hdr.Name != "manifest.json" {
			limit = maxDBSize
		}
		if hdr.Size > limit {
			return m, nil, nil, fmt.Errorf("%q is %d bytes, more than a backup holds", hdr.Name, hdr.Size)
		}
		body, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return m, nil, nil, fmt.Errorf("read %q: %w", hdr.Name, err)
		}
		bodies[hdr.Name] = body
	}
	if err := json.Unmarshal(bodies["manifest.json"], &m); err != nil || m.Format != Format {
		return m, nil, nil, errors.New("this archive is not a full backup of this panel")
	}
	if m.Version > Version {
		return m, nil, nil, fmt.Errorf("this backup was made by a newer panel (format %d); update this one first", m.Version)
	}
	if m.Database != "x-ui.db" && m.Database != "x-ui.dump" {
		return m, nil, nil, fmt.Errorf("the backup names its database %q", m.Database)
	}
	db, ok := bodies[m.Database]
	if !ok {
		return m, nil, nil, errors.New("the backup holds no database")
	}
	files := make([]File, 0, len(m.Files))
	for _, f := range m.Files {
		if _, err := cleanRel(f.Path); err != nil {
			return m, nil, nil, err
		}
		body, ok := bodies[path.Join("files", f.Kind, f.Path)]
		if !ok || digest(body) != f.SHA256 {
			return m, nil, nil, fmt.Errorf("%s/%s is missing or does not match the backup's record of it", f.Kind, f.Path)
		}
		f.data = body
		files = append(files, f)
	}
	return m, db, files, nil
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
			undos = append(undos, func() { _ = writeAtomic(target, old, 0o600) })
		case errors.Is(err, fs.ErrNotExist):
			undos = append(undos, func() { _ = os.Remove(target) })
		default:
			return undo, err
		}
		mode := f.Mode & 0o644
		if set.Private || mode == 0 {
			mode = 0o600
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return undo, err
		}
		if err := writeAtomic(target, f.data, mode); err != nil {
			return undo, fmt.Errorf("restore %s/%s: %w", f.Kind, f.Path, err)
		}
	}
	return undo, nil
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
