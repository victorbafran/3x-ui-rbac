package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"os"
	"os/exec"
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
// certificates, relay SSH keys, OpenVPN and cisco keys, the node-token and Arvan keys.
var backupSets = func() []fullbackup.Set {
	tokenKey := config.GetNodeTokenKeyFile()
	arvanKey := arvanKeyFile()
	return []fullbackup.Set{
		{Kind: "certs", Dir: certissue.Dir, Keep: notTemp, DirMode: 0o700},
		{Kind: "relay-keys", Dir: relayKeyDir, Keep: notTemp, Flat: true, Private: true, DirMode: 0o700},
		{Kind: "openvpn", Dir: openvpn.Dir, Keep: openvpnKeyFile.MatchString, Flat: true},
		{Kind: "ocserv", Dir: ocserv.Dir, Keep: ocservKeyFile.MatchString, Flat: true},
		{
			Kind: "node-token", Dir: func() string { return filepath.Dir(tokenKey) },
			Keep: func(rel string) bool { return rel == filepath.Base(tokenKey) }, Flat: true, Private: true, DirMode: 0o700,
		},
		{
			Kind: "arvan-key", Dir: func() string { return filepath.Dir(arvanKey) },
			Keep: func(rel string) bool { return rel == filepath.Base(arvanKey) }, Flat: true, Private: true, DirMode: 0o700,
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

// DatabaseReplaced reports an import error raised once the new database was
// in place, after which the panel still has to restart onto it.
func DatabaseReplaced(err error) bool { return errors.As(err, new(dbReplaced)) }

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
	// Before any file lands: the sidecars restart onto restored keys, and a
	// database refused afterwards would restart them twice for nothing.
	if err := checkStagedDB(staged); err != nil {
		return err
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
		if DatabaseReplaced(err) {
			logger.Warningf("restore: the new database is in place; the files it replaced are kept in %s", keepDir)
			return dbReplaced{fmt.Errorf("%w; the files it replaced are kept in %s", err, keepDir)}
		}
		undo()
		// The import may have restarted xray while the backup's certificates
		// were on disk; start it again on the files just put back.
		if errR := s.RestartXrayService(); errR != nil {
			logger.Warningf("restore: xray did not restart after the undo: %v", errR)
		}
		return err
	}
	if _, err := os.Stat(keepDir); err == nil {
		logger.Infof("restore: the files it replaced are kept in %s", keepDir)
	}
	return nil
}

// RestoreFullBackupOffline is importFullBackup for a stopped panel (`x-ui
// restore-full`), how a copy takes this panel's state; force replaces a database.
func RestoreFullBackupOffline(path string, force bool) error {
	if database.IsPostgres() {
		return common.NewError("restore-full needs a SQLite panel; this one runs PostgreSQL")
	}
	dbPath := config.GetDBPath()
	_, err := os.Stat(dbPath)
	hadDB := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if hadDB && !force {
		return common.NewErrorf("%s already exists; pass -force to replace it", dbPath)
	}
	in, err := os.Open(path)
	if err != nil {
		return common.NewErrorf("Error reading the backup: %v", err)
	}
	defer in.Close()
	staged := dbPath + ".archive"
	_, files, err := fullbackup.Read(in, staged)
	if err != nil {
		return common.NewErrorf("Invalid full backup: %v", err)
	}
	defer os.Remove(staged)
	if head, err := readHead(staged); err != nil || fullbackup.IsArchive(head) {
		return common.NewError("Invalid full backup: its database is itself an archive")
	}
	if err := checkStagedDB(staged); err != nil {
		return err
	}
	tempPath := dbPath + ".temp"
	defer removeSQLite(tempPath)
	if err := stageOfflineDB(staged, tempPath); err != nil {
		return err
	}

	keepDir := filepath.Join(config.GetDBFolderPath(), "pre-restore-"+time.Now().Format("20060102-150405"))
	undo, err := fullbackup.Restore(backupSets(), files, keepDir)
	if err != nil {
		undo()
		return common.NewErrorf("Error restoring the backup's files: %v", err)
	}
	fallback := dbPath + ".backup"
	if hadDB {
		removeSQLite(fallback)
		if err := moveSQLite(dbPath, fallback); err != nil {
			_ = moveSQLite(fallback, dbPath)
			undo()
			return common.NewErrorf("Error setting the current database aside: %v", err)
		}
	}
	// A journal left without its database would be replayed into the new one.
	removeSQLite(dbPath)
	putBack := func() {
		removeSQLite(dbPath)
		if hadDB {
			_ = moveSQLite(fallback, dbPath)
		}
		undo()
	}
	if err := os.Rename(tempPath, dbPath); err != nil {
		putBack()
		return common.NewErrorf("Error moving db file: %v", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		logger.Warningf("restore: could not make %s private: %v", dbPath, err)
	}
	if err := database.InitDB(dbPath); err != nil {
		// Closed before the rename back, or Windows refuses to replace the file.
		_ = database.CloseDB()
		putBack()
		return common.NewErrorf("Error migrating db: %v", err)
	}
	(&InboundService{}).MigrateDB()
	if err := database.CloseDB(); err != nil {
		logger.Warningf("restore: closing the restored database: %v", err)
	}
	if hadDB {
		removeSQLite(fallback)
	}
	if _, err := os.Stat(keepDir); err == nil {
		logger.Infof("restore: the files it replaced are kept in %s", keepDir)
	}
	return nil
}

// stageOfflineDB turns the backup's database into a SQLite file at tempPath,
// rebuilding it when the backup carries a SQLite text dump.
func stageOfflineDB(staged, tempPath string) error {
	f, err := os.Open(staged)
	if err != nil {
		return common.NewErrorf("Error reading the backup's database: %v", err)
	}
	defer f.Close()
	kind, err := sniffUploadKind(f)
	if err != nil {
		return common.NewErrorf("Error reading the backup's database: %v", err)
	}
	removeSQLite(tempPath)
	if err := stageSQLiteUpload(f, kind, tempPath); err != nil {
		return err
	}
	if err := database.ValidateSQLiteDB(tempPath); err != nil {
		return common.NewErrorf("Invalid or corrupt db file: %v", err)
	}
	if err := database.PrepareSQLiteForMigration(tempPath); err != nil {
		return common.NewErrorf("This file cannot be imported: %v", err)
	}
	return nil
}

// sqliteSuffixes are a SQLite database's file and the two its WAL journal keeps
// beside it, which hold its latest writes until a checkpoint.
var sqliteSuffixes = []string{"", "-wal", "-shm"}

// moveSQLite moves a database with its journal files onto a clear destination;
// missing ones are skipped, so a move that stopped half way can be moved back.
func moveSQLite(from, to string) error {
	for _, suffix := range sqliteSuffixes {
		if err := os.Rename(from+suffix, to+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func removeSQLite(path string) {
	for _, suffix := range sqliteSuffixes {
		_ = os.Remove(path + suffix)
	}
}

// checkStagedDB refuses, as ImportDB would, a database this panel cannot take.
func checkStagedDB(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return common.NewErrorf("Error reading the backup's database: %v", err)
	}
	defer f.Close()
	kind, err := sniffUploadKind(f)
	if err != nil {
		return common.NewErrorf("Error reading the backup's database: %v", err)
	}
	switch {
	case kind == importKindPgDump && database.IsPostgres():
		bin, err := exec.LookPath("pg_restore")
		if err != nil {
			return common.NewError("pg_restore not found on the server; install the postgresql-client package to restore a PostgreSQL database")
		}
		return checkPgRestoreCanRead(bin, path)
	case kind == importKindPgDump:
		return common.NewError("This file is a PostgreSQL backup; it can only be restored on a panel running PostgreSQL")
	case kind == importKindSQLiteDB:
		if err := database.ValidateSQLiteDB(path); err != nil {
			return common.NewErrorf("Invalid or corrupt db file: %v", err)
		}
		return nil
	case kind == importKindSQLiteDump:
		return nil
	}
	return common.NewError("Invalid full backup: its database is neither SQLite nor a PostgreSQL dump")
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
