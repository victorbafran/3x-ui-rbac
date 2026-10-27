package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/fullbackup"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/version"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// Copy status values, as stored and as the copies tab reads them.
const (
	ClonePending    = "pending"
	CloneInstalling = "installing"
	CloneStandby    = "standby"
	CloneMain       = "main"
	CloneError      = "error"

	cloneModeRelease = "release"
	cloneModeCopied  = "copied"
)

// Where a copy's pieces sit on the new server: install.sh's layout, plus a work
// folder whose marker says an unfinished install there is this panel's own.
const (
	cloneBinary   = "/usr/local/x-ui/x-ui"
	cloneScript   = "/usr/bin/x-ui"
	cloneDB       = "/etc/x-ui/x-ui.db"
	cloneWorkDir  = "/root/.x-ui-clone"
	cloneMarker   = cloneWorkDir + "/in-progress"
	cloneStateTar = cloneWorkDir + "/state.tar.gz"
)

const (
	cloneJobTimeout     = time.Hour
	cloneInstallTimeout = 20 * time.Minute
	cloneCheckTimeout   = time.Minute
	cloneMinFreeKB      = 1536 * 1024
)

// cloneExtraPackages go onto a copy when this server has them; install.sh
// brings the rest, and ocserv through XUI_WITH_CISCO.
var cloneExtraPackages = []string{"openvpn", "wireguard-tools", "iptables", "nftables", "fail2ban", "sqlite3"}

// PanelCloneService copies this panel onto other servers and keeps those copies
// on standby until one is made the main panel.
type PanelCloneService struct{}

// PanelCloneRequest is the add form. Password and PrivateKey are used once, to
// install the panel's own key, and never stored.
type PanelCloneRequest struct {
	Address    string `json:"address"`
	SSHPort    int    `json:"sshPort"`
	SSHUser    string `json:"sshUser"`
	Password   string `json:"password"`
	PrivateKey string `json:"privateKey"`
}

var errCloneBusy = errors.New("this copy is busy with another job; wait for it to finish")

// cloneBusy holds the copies with a job running. A row that says it is working
// but is not in here was cut off by a restart of this panel.
var (
	cloneMu   sync.Mutex
	cloneBusy = map[int]bool{}
)

func claimClone(id int) bool {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	if cloneBusy[id] {
		return false
	}
	cloneBusy[id] = true
	return true
}

func releaseClone(id int) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	delete(cloneBusy, id)
}

// List returns every copy, first settling any job this panel's restart cut off
// so its row does not say it is working for ever.
func (s *PanelCloneService) List() ([]model.PanelClone, error) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	var rows []model.PanelClone
	if err := database.GetDB().Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		r := &rows[i]
		if cloneBusy[r.Id] || (r.Step == "" && r.Status != ClonePending && r.Status != CloneInstalling) {
			continue
		}
		r.LastError = "this panel restarted before the job finished"
		if r.Step != "" {
			r.LastError += " (at: " + r.Step + ")"
		}
		r.Step = ""
		if r.Status == ClonePending || r.Status == CloneInstalling {
			r.Status = CloneError
		}
		s.update(r.Id, map[string]any{"status": r.Status, "step": "", "last_error": r.LastError})
	}
	return rows, nil
}

// Add records the copy and installs it in the background; the row's step
// tells the page how far it got.
func (s *PanelCloneService) Add(req PanelCloneRequest) (*model.PanelClone, error) {
	if global.IsStandby() {
		return nil, global.ErrStandby
	}
	if err := cloneSourceUsable(); err != nil {
		return nil, err
	}
	address := strings.TrimSpace(req.Address)
	if address == "" || strings.ContainsAny(address, " \t/") {
		return nil, fmt.Errorf("%q is not a server address", req.Address)
	}
	port := req.SSHPort
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("SSH port %d is out of range", port)
	}
	user := strings.TrimSpace(req.SSHUser)
	if user == "" {
		user = "root"
	}
	if strings.TrimSpace(req.Password) == "" && strings.TrimSpace(req.PrivateKey) == "" {
		return nil, errors.New("a password or a private key is needed once, to install the panel's own key")
	}
	var same int64
	if err := database.GetDB().Model(&model.PanelClone{}).Where("address = ?", address).Count(&same).Error; err != nil {
		return nil, err
	}
	if same > 0 {
		return nil, fmt.Errorf("%s is already a copy of this panel; remove it first to start again", address)
	}

	row := &model.PanelClone{Address: address, SSHPort: port, SSHUser: user, Status: ClonePending}
	cloneMu.Lock()
	err := database.GetDB().Create(row).Error
	if err == nil {
		cloneBusy[row.Id] = true
	}
	cloneMu.Unlock()
	if err != nil {
		return nil, err
	}
	row.KeyPath = filepath.Join(relayKeyDir(), fmt.Sprintf("clone-%d", row.Id))
	if err := database.GetDB().Model(row).Update("key_path", row.KeyPath).Error; err != nil {
		releaseClone(row.Id)
		return nil, err
	}
	id, password, privateKey := row.Id, req.Password, req.PrivateKey
	common.GoRecover("panel copy install", func() {
		defer releaseClone(id)
		s.install(id, password, privateKey)
	})
	return row, nil
}

func (s *PanelCloneService) install(id int, password, privateKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), cloneJobTimeout)
	defer cancel()
	row, err := loadClone(id)
	if err != nil {
		return
	}
	s.update(id, map[string]any{"status": CloneInstalling, "last_error": ""})

	s.step(id, "Installing this panel's SSH key on the server")
	creds := relay.Credentials{Address: row.Address, Port: row.SSHPort, User: row.SSHUser, Password: password}
	if strings.TrimSpace(privateKey) != "" {
		creds.PrivateKey = []byte(privateKey)
	}
	if err := relay.Enroll(ctx, creds, row.KeyPath); err != nil {
		s.fail(row, err)
		return
	}
	cl, err := dialClone(ctx, row)
	if err != nil {
		s.fail(row, err)
		return
	}
	defer cl.Close()

	s.step(id, "Checking the server")
	if err := checkCloneTarget(ctx, cl); err != nil {
		s.fail(row, err)
		return
	}
	s.step(id, "Installing the panel's latest release")
	if err := installCloneRelease(ctx, cl); err != nil {
		s.fail(row, err)
		return
	}
	s.step(id, "Installing the packages this server uses")
	if err := installClonePackages(ctx, cl); err != nil {
		s.fail(row, err)
		return
	}

	s.step(id, "Choosing between the release and this panel's build")
	latest, lerr := latestForkRelease()
	if lerr != nil {
		logger.Warningf("panel copy %s: could not read the latest release, so this panel's build decides: %v", row.Address, lerr)
	}
	copied, same, err := alignCloneBinary(ctx, cl, latest, lerr == nil)
	if err != nil {
		s.fail(row, err)
		return
	}
	mode, ver := cloneModeRelease, latest
	if copied || same || lerr != nil {
		ver = config.GetReleaseTag()
	}
	if copied {
		mode = cloneModeCopied
	}

	warning, err := s.restoreOnto(ctx, cl, id)
	if err != nil {
		s.fail(row, err)
		return
	}
	now := time.Now().Unix()
	s.update(id, map[string]any{
		"status": CloneStandby, "step": "", "mode": mode, "version": ver,
		"last_error": warning, "last_sync": now, "last_seen": now,
	})
	logger.Infof("panel copy at %s is installed and on standby (%s, %s)", row.Address, mode, ver)
}

// Sync puts this panel's current data onto a standby copy.
func (s *PanelCloneService) Sync(id int) error {
	row, err := s.startJob(id)
	if err != nil {
		return err
	}
	common.GoRecover("panel copy sync", func() {
		defer releaseClone(id)
		ctx, cancel := context.WithTimeout(context.Background(), cloneJobTimeout)
		defer cancel()
		cl, err := dialClone(ctx, row)
		if err != nil {
			s.fail(row, err)
			return
		}
		defer cl.Close()
		if err := s.sync(ctx, cl, row); err == nil {
			logger.Infof("panel copy at %s is synced", row.Address)
		}
	})
	return nil
}

// Promote syncs a copy, makes it the main panel and only then puts this panel
// on standby; a copy that will not run as main goes back on standby.
func (s *PanelCloneService) Promote(id int) error {
	row, err := s.startJob(id)
	if err != nil {
		return err
	}
	common.GoRecover("panel copy promote", func() {
		defer releaseClone(id)
		ctx, cancel := context.WithTimeout(context.Background(), cloneJobTimeout)
		defer cancel()
		cl, err := dialClone(ctx, row)
		if err != nil {
			s.fail(row, err)
			return
		}
		defer cl.Close()
		if err := s.sync(ctx, cl, row); err != nil {
			return
		}
		// a copy that cannot reach the relays would cut every tunnel once main
		s.step(id, "Checking that the copy reaches every relay")
		if blocked, err := cloneUnreachedRelays(ctx, cl); err != nil || blocked != "" {
			if err == nil {
				err = fmt.Errorf("the copy cannot reach %s; its provider may block these ports, so it stays on standby and this panel stays main", blocked)
			}
			s.fail(row, err)
			s.update(id, map[string]any{"status": CloneStandby})
			return
		}
		s.step(id, "Making the copy the main panel")
		if err := promoteClone(ctx, cl); err != nil {
			s.putBack(ctx, cl, row, "it could not run as the main panel", err)
			return
		}
		s.update(id, map[string]any{"status": CloneMain, "step": "", "last_error": "", "last_seen": time.Now().Unix()})
		if err := SetStandbyMode(true); err != nil {
			s.putBack(ctx, cl, row, "this panel could not go on standby", err)
			return
		}
		logger.Infof("panel copy at %s is now the main panel; this panel is on standby", row.Address)
	})
	return nil
}

// Check asks a copy whether its panel runs and whether it is on standby.
func (s *PanelCloneService) Check(id int) (*model.PanelClone, error) {
	row, err := loadClone(id)
	if err != nil {
		return nil, err
	}
	if !claimClone(id) {
		return nil, errCloneBusy
	}
	defer releaseClone(id)
	ctx, cancel := context.WithTimeout(context.Background(), cloneCheckTimeout)
	defer cancel()

	cl, err := dialClone(ctx, row)
	if err != nil {
		s.update(id, map[string]any{"last_error": err.Error()})
		return nil, err
	}
	defer cl.Close()
	out, err := cl.Run(ctx, strings.Join([]string{
		`printf 'active=%s\n' "$(systemctl is-active x-ui 2>/dev/null)"`,
		`printf 'standby=%s\n' "$(` + cloneBinary + ` standby status 2>/dev/null | tail -n 1)"`,
		`printf 'version=%s\n' "$(` + cloneBinary + ` -v 2>/dev/null | tail -n 1)"`,
	}, "; "))
	if err != nil {
		s.update(id, map[string]any{"last_error": err.Error()})
		return nil, err
	}
	facts := cloneFacts(out)
	fields := map[string]any{"last_seen": time.Now().Unix(), "version": freshCloneVersion(row.Version, facts["version"])}
	on, known := parseStandby(facts["standby"])
	switch {
	case facts["active"] != "active":
		fields["status"] = CloneError
		fields["last_error"] = fmt.Sprintf("x-ui is not running on the copy (%s)", facts["active"])
	case !known:
		fields["last_error"] = fmt.Sprintf("the copy did not say whether it is on standby (%q)", facts["standby"])
	case on:
		fields["status"], fields["last_error"] = CloneStandby, ""
	default:
		fields["status"], fields["last_error"] = CloneMain, ""
	}
	if fields["status"] == CloneStandby || fields["status"] == CloneMain {
		if blocked, err := cloneUnreachedRelays(ctx, cl); err != nil {
			fields["last_error"] = "could not test the relays from the copy: " + err.Error()
		} else if blocked != "" {
			fields["last_error"] = "the copy cannot reach " + blocked + "; it must not be made main from this server"
		}
	}
	s.update(id, fields)
	return loadClone(id)
}

// cloneUnreachedRelays dials every enabled relay's SSH port, and a front's
// tunnel port, from the copy; a provider that filters ports shows up here.
func cloneUnreachedRelays(ctx context.Context, cl *relay.Client) (string, error) {
	var relays []model.Relay
	if err := database.GetDB().Where("enabled = ?", true).Order("id").Find(&relays).Error; err != nil {
		return "", err
	}
	type target struct {
		label, host string
		port        int
	}
	var targets []target
	for _, r := range relays {
		// the address lands in a shell line on the copy, so only plain hosts pass
		if strings.Trim(r.Address, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-:") != "" {
			continue
		}
		targets = append(targets, target{r.Name + " ssh", r.Address, r.SSHPort})
		if r.Role == model.RelayFront && r.TunnelPort > 0 {
			targets = append(targets, target{r.Name + " tunnel", r.Address, r.TunnelPort})
		}
	}
	if len(targets) == 0 {
		return "", nil
	}
	var script strings.Builder
	for _, t := range targets {
		fmt.Fprintf(&script, "timeout 6 bash -c 'exec 3<>/dev/tcp/%s/%d' 2>/dev/null || echo %s\n",
			t.host, t.port, cloneQuote(fmt.Sprintf("%s %s", t.label, net.JoinHostPort(t.host, strconv.Itoa(t.port)))))
	}
	out, err := cl.Run(ctx, "bash -c "+cloneQuote(script.String()))
	if err != nil {
		return "", err
	}
	var blocked []string
	for line := range strings.Lines(out) {
		if line = strings.TrimSpace(line); line != "" {
			blocked = append(blocked, line)
		}
	}
	return strings.Join(blocked, ", "), nil
}

// Delete forgets a copy: its row and its key. The server itself is left as it is.
func (s *PanelCloneService) Delete(id int) error {
	if !claimClone(id) {
		return errCloneBusy
	}
	defer releaseClone(id)
	row, err := loadClone(id)
	if err != nil {
		return err
	}
	if err := database.GetDB().Delete(&model.PanelClone{}, id).Error; err != nil {
		return err
	}
	if row.KeyPath != "" {
		if err := os.Remove(row.KeyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warningf("panel copy %s: its key %s stays behind: %v", row.Address, row.KeyPath, err)
		}
	}
	return nil
}

// startJob is what Sync and Promote share: refuse on standby or on a copy
// that never finished, then take the copy's slot.
func (s *PanelCloneService) startJob(id int) (*model.PanelClone, error) {
	if global.IsStandby() {
		return nil, global.ErrStandby
	}
	if err := cloneSourceUsable(); err != nil {
		return nil, err
	}
	row, err := loadClone(id)
	if err != nil {
		return nil, err
	}
	if row.LastSync == 0 {
		return nil, errors.New("this copy never finished installing; remove it and add it again")
	}
	if row.Status == CloneMain {
		return nil, errors.New("this copy is the main panel now; syncing would overwrite it with this panel's older data")
	}
	if !claimClone(id) {
		return nil, errCloneBusy
	}
	return row, nil
}

// sync refuses a copy that has become a main panel, brings its program up to
// this panel's build when that is not older, and restores this panel's data.
func (s *PanelCloneService) sync(ctx context.Context, cl *relay.Client, row *model.PanelClone) error {
	s.step(row.Id, "Checking the copy")
	on, err := cloneStandbyState(ctx, cl)
	if err != nil {
		s.fail(row, err)
		return err
	}
	if !on {
		err := fmt.Errorf("the copy at %s runs as a main panel; syncing would overwrite it", row.Address)
		s.update(row.Id, map[string]any{"status": CloneMain, "step": "", "last_error": err.Error()})
		return err
	}
	verOut, _ := cl.Run(ctx, cloneBinary+" -v 2>/dev/null | tail -n 1")
	ver := freshCloneVersion(row.Version, verOut)

	s.step(row.Id, "Comparing the copy's program with this panel's")
	copied, _, err := alignCloneBinary(ctx, cl, ver, ver != "")
	if err != nil {
		s.fail(row, err)
		return err
	}
	warning, err := s.restoreOnto(ctx, cl, row.Id)
	if err != nil {
		s.fail(row, err)
		return err
	}
	now := time.Now().Unix()
	fields := map[string]any{
		"status": CloneStandby, "step": "", "version": ver,
		"last_error": warning, "last_sync": now, "last_seen": now,
	}
	if copied {
		fields["mode"], fields["version"] = cloneModeCopied, config.GetReleaseTag()
	}
	s.update(row.Id, fields)
	return nil
}

// restoreOnto replaces the copy's data with this panel's. The standby marker
// goes down before anything else, so no failure past it leaves a second main.
func (s *PanelCloneService) restoreOnto(ctx context.Context, cl *relay.Client, id int) (warning string, err error) {
	s.step(id, "Stopping the copy and keeping it on standby")
	if _, err := cl.Run(ctx, "systemctl stop x-ui && "+cloneBinary+" standby on"); err != nil {
		return "", fmt.Errorf("could not stop the copy and put it on standby: %w", err)
	}

	s.step(id, "Sending this panel's data")
	archive, err := cloneArchiveFor(id)
	if err != nil {
		return "", fmt.Errorf("could not make this panel's backup: %w", err)
	}
	if _, err := cl.Run(ctx, "mkdir -p -m 700 "+cloneWorkDir); err != nil {
		return "", err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = cl.Run(cctx, "rm -f "+cloneStateTar)
	}()
	if err := cl.UploadFrom(ctx, cloneStateTar, bytes.NewReader(archive)); err != nil {
		return "", fmt.Errorf("could not send the backup: %w", err)
	}

	s.step(id, "Restoring the data on the copy")
	// Its verdict is the last line on stdout; stderr carries the logger.
	if out, err := cl.RunLong(ctx, cloneBinary+" restore-full -force "+cloneStateTar, nil); err != nil {
		return "", fmt.Errorf("the copy could not restore the backup: %s (%w)", cloneTail(out, 1), err)
	}
	// After the restore: the jail's exemptions read the copied panel's port.
	if fail2banActiveHere() {
		s.step(id, "Setting up fail2ban on the copy")
		if _, ferr := cl.RunLong(ctx, cloneScript+" setup-fail2ban < /dev/null", nil); ferr != nil {
			warning = "fail2ban could not be set up on the copy: " + ferr.Error()
		}
	}

	s.step(id, "Starting the copy on standby")
	if _, err := cl.Run(ctx, "systemctl start x-ui"); err != nil {
		return "", fmt.Errorf("x-ui would not start on the copy: %w", err)
	}
	if err := waitCloneActive(ctx, cl); err != nil {
		return "", err
	}
	// Unconfirmed counts as main: a copy holding this panel's data must not run as one.
	on, err := cloneStandbyState(ctx, cl)
	if err == nil && !on {
		err = errors.New("the copy started as a main panel, so it was stopped again")
	}
	if err != nil {
		_, _ = cl.Run(context.WithoutCancel(ctx), "systemctl stop x-ui")
		return "", err
	}
	if _, err := cl.Run(ctx, "rm -rf "+cloneWorkDir); err != nil {
		logger.Warningf("panel copy %d: could not remove %s: %v", id, cloneWorkDir, err)
	}
	return warning, nil
}

// promoteClone takes a copy off standby and waits until it runs as main.
func promoteClone(ctx context.Context, cl *relay.Client) error {
	if _, err := cl.Run(ctx, cloneBinary+" standby off && systemctl restart x-ui"); err != nil {
		return err
	}
	if err := waitCloneActive(ctx, cl); err != nil {
		return err
	}
	on, err := cloneStandbyState(ctx, cl)
	if err != nil {
		return err
	}
	if on {
		return errors.New("the copy still says it is on standby")
	}
	return nil
}

// putBack returns a copy to standby after a failed promotion; this panel has
// not gone on standby, so it stays the main one.
func (s *PanelCloneService) putBack(ctx context.Context, cl *relay.Client, row *model.PanelClone, what string, cause error) {
	// Its own deadline: a job that ran out of time must still not leave two mains.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	_, err := cl.Run(ctx, cloneBinary+" standby on && systemctl restart x-ui")
	if err == nil {
		err = waitCloneActive(ctx, cl)
	}
	if err != nil {
		s.fail(row, fmt.Errorf("%s (%w), and putting the copy back on standby failed too: %w", what, cause, err))
		return
	}
	msg := fmt.Sprintf("%s (%v), so the copy is back on standby and this panel stays main", what, cause)
	s.update(row.Id, map[string]any{"status": CloneStandby, "step": "", "last_error": msg})
	logger.Warningf("panel copy at %s: %s", row.Address, msg)
}

// cloneSourceUsable refuses what a copy cannot be made from.
func cloneSourceUsable() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("a panel can only be copied from a Linux server, not %s", runtime.GOOS)
	}
	if database.IsPostgres() {
		return errors.New("a panel that keeps its data in PostgreSQL cannot be copied; a copy restores a SQLite backup")
	}
	return nil
}

func loadClone(id int) (*model.PanelClone, error) {
	var row model.PanelClone
	if err := database.GetDB().First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func dialClone(ctx context.Context, row *model.PanelClone) (*relay.Client, error) {
	key, err := os.ReadFile(row.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("the key for the copy at %s is missing; remove it and add it again: %w", row.Address, err)
	}
	return relay.Dial(ctx, relay.Credentials{Address: row.Address, Port: row.SSHPort, User: row.SSHUser, PrivateKey: key})
}

func (s *PanelCloneService) update(id int, fields map[string]any) {
	if err := database.GetDB().Model(&model.PanelClone{Id: id}).Updates(fields).Error; err != nil {
		logger.Warningf("panel copy %d: could not record its state: %v", id, err)
	}
}

func (s *PanelCloneService) step(id int, label string) {
	s.update(id, map[string]any{"step": label})
}

func (s *PanelCloneService) fail(row *model.PanelClone, err error) {
	s.update(row.Id, map[string]any{"status": CloneError, "step": "", "last_error": err.Error()})
	logger.Warningf("panel copy at %s: %v", row.Address, err)
}

// checkCloneTarget reads the new server's facts and refuses one a copy would
// not run on, or one that already holds a panel this one did not start.
func checkCloneTarget(ctx context.Context, cl *relay.Client) error {
	machine, err := localMachine()
	if err != nil {
		return fmt.Errorf("could not read this server's architecture: %w", err)
	}
	script := `#!/bin/sh
echo "uid=$(id -u)"
echo "arch=$(uname -m)"
if [ -r /etc/os-release ]; then . /etc/os-release; fi
echo "os=${ID:-}"
echo "like=${ID_LIKE:-}"
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then echo systemd=yes; else echo systemd=no; fi
echo "freekb=$(df -Pk / | awk 'NR==2 {print $4}')"
if [ -e ` + cloneDB + ` ]; then echo db=yes; else echo db=no; fi
if [ -e ` + cloneMarker + ` ]; then echo ours=yes; else echo ours=no; fi`
	out, err := cl.RunScript(ctx, "xui-clone-check.sh", script)
	if err != nil {
		return fmt.Errorf("could not check the server: %w", err)
	}
	return cloneTargetProblem(cloneFacts(out), machine)
}

func cloneTargetProblem(f map[string]string, machine string) error {
	free, ferr := strconv.ParseInt(f["freekb"], 10, 64)
	switch {
	case f["uid"] != "0":
		return errors.New("a copy has to be installed as root; log in as root")
	case f["arch"] != machine:
		return fmt.Errorf("the server is %s but this one is %s, so this panel's program would not run there", f["arch"], machine)
	case !cloneDebianLike(f["os"], f["like"]):
		return fmt.Errorf("the server runs %q; a copy needs Debian or Ubuntu", f["os"])
	case f["systemd"] != "yes":
		return errors.New("the server does not run systemd, which the panel's service needs")
	case ferr != nil:
		return fmt.Errorf("could not read the free space on the server (%q)", f["freekb"])
	case free < cloneMinFreeKB:
		return fmt.Errorf("the server has %.1f GB free on /; a copy needs at least 1.5 GB", float64(free)/(1024*1024))
	case f["db"] == "yes" && f["ours"] != "yes":
		return fmt.Errorf("a panel is already installed there (%s exists); a copy never replaces one", cloneDB)
	}
	return nil
}

func cloneDebianLike(id, like string) bool {
	for _, name := range append([]string{id}, strings.Fields(like)...) {
		if name == "debian" || name == "ubuntu" {
			return true
		}
	}
	return false
}

// installCloneRelease runs the fork's own installer unattended. The marker goes
// down first so a retry after a failed install is not taken for a foreign panel.
func installCloneRelease(ctx context.Context, cl *relay.Client) error {
	repo, branch := config.UpdateRepo(), config.UpdateBranch()
	cisco := "0"
	if ocserv.ServerBinaryPath() != "" {
		cisco = "1"
	}
	script := cloneWorkDir + "/install.sh"
	cmd := "mkdir -p -m 700 " + cloneWorkDir + " && touch " + cloneMarker +
		" && curl -fsSL --retry 3 -o " + script + " " + cloneQuote("https://raw.githubusercontent.com/"+repo+"/"+branch+"/install.sh") +
		" && XUI_NONINTERACTIVE=1 XUI_SSL_MODE=none XUI_WITH_CISCO=" + cisco + " XUI_ENABLE_FAIL2BAN=0" +
		" XUI_REPO=" + cloneQuote(repo) + " XUI_BRANCH=" + cloneQuote(branch) + " bash " + script + " < /dev/null"
	ictx, cancel := context.WithTimeout(ctx, cloneInstallTimeout)
	defer cancel()
	if out, err := cl.RunLong(ictx, cmd, nil); err != nil {
		return fmt.Errorf("the install failed: %w; it ended with: %s", err, cloneTail(out, 3))
	}
	return nil
}

func installClonePackages(ctx context.Context, cl *relay.Client) error {
	pkgs := localPackages(cloneExtraPackages)
	if len(pkgs) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, cloneInstallTimeout)
	defer cancel()
	cmd := "DEBIAN_FRONTEND=noninteractive apt-get install -y -q " + strings.Join(pkgs, " ") + " < /dev/null"
	if out, err := cl.RunLong(pctx, cmd, nil); err != nil {
		return fmt.Errorf("could not install %s: %w; apt ended with: %s", strings.Join(pkgs, ", "), err, cloneTail(out, 3))
	}
	return nil
}

// alignCloneBinary puts this panel's own program over the copy's when
// cloneBuildWins says so, and checks it arrived whole before it is used.
func alignCloneBinary(ctx context.Context, cl *relay.Client, theirs string, theirsKnown bool) (copied, same bool, err error) {
	exe, err := os.Executable()
	if err != nil {
		return false, false, fmt.Errorf("could not find this panel's program: %w", err)
	}
	local, err := cloneFileSum(exe)
	if err != nil {
		return false, false, fmt.Errorf("could not read this panel's program: %w", err)
	}
	remote, err := remoteCloneSum(ctx, cl, cloneBinary)
	if err != nil {
		return false, false, err
	}
	same = remote == local
	if !cloneBuildWins(config.GetReleaseTag(), theirs, theirsKnown, same) {
		return false, same, nil
	}

	f, err := os.Open(exe)
	if err != nil {
		return false, false, fmt.Errorf("could not read this panel's program: %w", err)
	}
	defer f.Close()
	staged := cloneBinary + ".new"
	if err := cl.UploadFrom(ctx, staged, f); err != nil {
		return false, false, fmt.Errorf("could not send this panel's program: %w", err)
	}
	if got, err := remoteCloneSum(ctx, cl, staged); err != nil || got != local {
		_, _ = cl.Run(ctx, "rm -f "+staged)
		if err == nil {
			err = errors.New("its checksum does not match")
		}
		return false, false, fmt.Errorf("this panel's program arrived damaged on the copy: %w", err)
	}
	if _, err := cl.Run(ctx, "chmod 755 "+staged+" && mv -f "+staged+" "+cloneBinary); err != nil {
		return false, false, fmt.Errorf("could not put this panel's program in place: %w", err)
	}
	return true, true, nil
}

// cloneBuildWins reports whether this panel's program should replace a copy's:
// never when they are one file, else whenever this build is not the older.
func cloneBuildWins(this, theirs string, theirsKnown, same bool) bool {
	if same {
		return false
	}
	if !theirsKnown {
		return true
	}
	cmp, ok := version.CompareRelease(this, theirs)
	if ok && !strings.Contains(version.Normalize(this), "-") {
		// A hand-built program carries only its base version, no fork number.
		base, _, _ := strings.Cut(version.Normalize(theirs), "-")
		cmp, ok = version.Compare(this, base)
	}
	return !ok || cmp >= 0
}

// freshCloneVersion keeps the tag stored for a copy until the copy reports
// another base version; its -v prints the base without the fork number.
func freshCloneVersion(stored, reported string) string {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return stored
	}
	base, _, _ := strings.Cut(version.Normalize(stored), "-")
	if base == version.Normalize(reported) {
		return stored
	}
	return reported
}

func remoteCloneSum(ctx context.Context, cl *relay.Client, path string) (string, error) {
	out, err := cl.Run(ctx, "sha256sum "+path)
	if err != nil {
		return "", fmt.Errorf("could not read %s on the copy: %w", path, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("sha256sum printed nothing for %s", path)
	}
	return strings.ToLower(fields[0]), nil
}

// cloneStandbyState asks the copy's own program, which reads the marker where
// its panel does.
func cloneStandbyState(ctx context.Context, cl *relay.Client) (bool, error) {
	out, err := cl.Run(ctx, cloneBinary+" standby status")
	if err != nil {
		return false, fmt.Errorf("could not ask the copy whether it is on standby: %w", err)
	}
	on, known := parseStandby(out)
	if !known {
		return false, fmt.Errorf("the copy did not say whether it is on standby (%q)", strings.TrimSpace(out))
	}
	return on, nil
}

func parseStandby(out string) (on, known bool) {
	switch {
	case strings.Contains(out, "standby: on"):
		return true, true
	case strings.Contains(out, "standby: off"):
		return false, true
	}
	return false, false
}

// waitCloneActive wants x-ui active on two reads five seconds apart, so a panel
// that dies right after starting is not taken for a running one.
func waitCloneActive(ctx context.Context, cl *relay.Client) error {
	deadline := time.Now().Add(90 * time.Second)
	state, streak := "", 0
	for time.Now().Before(deadline) {
		out, _ := cl.Run(ctx, "systemctl is-active x-ui")
		state = strings.TrimSpace(out)
		if state == "active" {
			streak++
			if streak == 2 {
				return nil
			}
		} else {
			streak = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	journal, _ := cl.Run(ctx, "journalctl -u x-ui -n 3 --no-pager -o cat")
	return fmt.Errorf("x-ui did not stay running on the copy (%s): %s", state, cloneTail(journal, 3))
}

// cloneArchiveFor is GetFullBackup without the copy's own row, so a copy never
// lists itself and cannot be told to stop its own panel.
func cloneArchiveFor(selfId int) ([]byte, error) {
	if database.IsPostgres() {
		return nil, errors.New("a PostgreSQL panel cannot be copied")
	}
	path, cleanup, err := (&ServerService{}).backupSQLite()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := database.DeleteFromSQLiteFile(path, &model.PanelClone{}, selfId); err != nil {
		return nil, fmt.Errorf("could not leave the copy's own row out: %w", err)
	}
	db, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	files, err := fullbackup.Collect(backupSets())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := fullbackup.Write(&buf, "x-ui.db", db, files, time.Now(), config.GetPanelVersion()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// latestForkRelease reads the fork's newest release tag where the panel's
// updater reads it; a variable so a test can answer without GitHub.
var latestForkRelease = func() (string, error) {
	url := "https://api.github.com/repos/" + config.UpdateRepo() + "/releases/latest"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&SettingService{}).NewProxiedHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, resp.Status)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", errors.New("the latest release has no tag")
	}
	return release.TagName, nil
}

// Facts about this server, variables so a test can stand in for them.
var (
	localMachine = func() (string, error) {
		out, err := exec.Command("uname", "-m").Output()
		return strings.TrimSpace(string(out)), err
	}
	localPackages = func(names []string) []string {
		var have []string
		for _, name := range names {
			out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", name).Output()
			if err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), "installed") {
				have = append(have, name)
			}
		}
		return have
	}
	fail2banActiveHere = func() bool {
		return exec.Command("systemctl", "is-active", "--quiet", "fail2ban").Run() == nil
	}
)

func cloneFileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cloneFacts reads the key=value lines the copy's checks print.
func cloneFacts(out string) map[string]string {
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			facts[k] = strings.TrimSpace(v)
		}
	}
	return facts
}

func cloneQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cloneTail keeps the end of a long install log, where its error is.
func cloneTail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
