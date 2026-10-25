package service

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certissue"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/fullbackup"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

var (
	openvpnKeyFile = regexp.MustCompile(`^(ca-\d+\.pem|ca-\d+\.pem\.key|cert-\d+\.pem|key-\d+\.pem|tls-crypt-\d+\.key)$`)
	ocservKeyFile  = regexp.MustCompile(`^(cert|key)-\d+\.pem$`)
)

func notTemp(rel string) bool { return !strings.HasSuffix(rel, ".tmp") }

// backupSets is what a full backup carries beside the database: issued
// certificates, relay SSH keys, OpenVPN and cisco keys, the node-token key.
var backupSets = func() []fullbackup.Set {
	tokenKey := config.GetNodeTokenKeyFile()
	return []fullbackup.Set{
		{Kind: "certs", Dir: certissue.Dir, Keep: notTemp, DirMode: 0o700},
		{Kind: "relay-keys", Dir: relayKeyDir, Keep: notTemp, Flat: true, Private: true, DirMode: 0o700},
		{Kind: "openvpn", Dir: openvpn.Dir, Keep: openvpnKeyFile.MatchString, Flat: true},
		{Kind: "ocserv", Dir: ocserv.Dir, Keep: ocservKeyFile.MatchString, Flat: true},
		{
			Kind: "node-token", Dir: func() string { return filepath.Dir(tokenKey) },
			Keep: func(rel string) bool { return rel == filepath.Base(tokenKey) }, Flat: true, Private: true, DirMode: 0o700,
		},
	}
}

// isFullBackup reports whether an upload is a full backup rather than a bare
// database.
func isFullBackup(file multipart.File) (bool, error) {
	head := make([]byte, 2)
	n, err := file.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return fullbackup.IsArchive(head[:n]), nil
}

// GetFullBackup is GetDb's archive: the database and every file it relies on.
func (s *ServerService) GetFullBackup() ([]byte, error) {
	db, err := s.GetDb()
	if err != nil {
		return nil, err
	}
	name := "x-ui.db"
	if database.IsPostgres() {
		name = "x-ui.dump"
	}
	files, err := fullbackup.Collect(backupSets())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := fullbackup.Write(&buf, name, db, files, time.Now(), config.GetPanelVersion()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FullBackupFilename is BackupFilename for the archive.
func (s *ServerService) FullBackupFilename(requestHost string) string {
	name := s.BackupFilename(requestHost)
	return strings.TrimSuffix(strings.TrimSuffix(name, ".db"), ".dump") + "-full.tar.gz"
}

// dbReplaced marks an import error raised once the new database is in place:
// the files restored beside it belong to it and stay.
type dbReplaced struct{ error }

func (e dbReplaced) Unwrap() error { return e.error }

// importStaged stands in for ImportDB when a test sets it, to fail after the swap.
var importStaged func(*ServerService, multipart.File, bool) error

// importFullBackup restores the files first, so no sidecar regenerates a key
// before its backup lands, then the database; a failed import undoes the files.
func (s *ServerService) importFullBackup(file multipart.File, keepHostSettings bool) error {
	staged := config.GetDBPath() + ".archive"
	_, files, err := fullbackup.Read(file, staged)
	if err != nil {
		return common.NewErrorf("Invalid full backup: %v", err)
	}
	defer os.Remove(staged)
	if head, err := readHead(staged); err != nil || fullbackup.IsArchive(head) {
		return common.NewError("Invalid full backup: its database is itself an archive")
	}

	keepDir := filepath.Join(config.GetDBFolderPath(), "pre-restore-"+time.Now().Format("20060102-150405"))
	undo, err := fullbackup.Restore(backupSets(), files, keepDir)
	if err != nil {
		undo()
		return common.NewErrorf("Error restoring the backup's files: %v", err)
	}
	f, err := os.Open(staged)
	if err != nil {
		undo()
		return common.NewErrorf("Error reading the backup's database: %v", err)
	}
	defer f.Close()
	run := importStaged
	if run == nil {
		run = (*ServerService).ImportDB
	}
	if err := run(s, f, keepHostSettings); err != nil {
		if true {
			undo()
		}
		return err
	}
	if _, err := os.Stat(keepDir); err == nil {
		logger.Infof("restore: the files it replaced are kept in %s", keepDir)
	}
	return nil
}

func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 2)
	n, err := io.ReadFull(f, head)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		err = nil
	}
	return head[:n], err
}
