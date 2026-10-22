package openvpn

import (
	"fmt"
	"os"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// The manager holds the running sidecars and moves them toward what the panel
// wants, the same way internal/ocserv does for cisco.

type managed struct {
	proc         *sidecar.Process
	tag          string
	cidr         string
	table        int
	exit         string
	structuralFP string
	usersFP      string
	// loginToEmail turns what the status file reports back into the panel
	// identity traffic is billed against; the two differ for every reseller
	// client, whose email carries a namespace the login cannot.
	loginToEmail map[string]string
	// limits is the device cap per login, consulted when deciding who is over.
	limits map[string]int
	// last is keyed by the process's own client id, not by login. A reconnect
	// is a new id whose counters start at zero; keying by login would read
	// that reset as a negative delta and silently drop the traffic.
	last map[string]counters
}

type counters struct {
	received int64
	sent     int64
}

type Manager struct {
	mu    sync.Mutex
	procs map[int]*managed
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the process-wide OpenVPN manager.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = &Manager{procs: map[int]*managed{}} })
	return manager
}

// action is what Ensure must do to move a running instance to the wanted one.
type action int

const (
	actionNone action = iota
	// actionUsers rewrites the credentials file in place. The helper reads it
	// on every connection, so adding, removing or re-keying a client needs no
	// restart and drops nobody who is already online.
	actionUsers
	actionRestart
)

func actionFor(running bool, curStructFP, curUsersFP, newStructFP, newUsersFP string) action {
	if !running || curStructFP != newStructFP {
		return actionRestart
	}
	if curUsersFP != newUsersFP {
		return actionUsers
	}
	return actionNone
}

// HasRunning reports whether anything is up, so a host with no OpenVPN
// inbound and no openvpn installed does not log a failure every reconcile.
func (m *Manager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		if p.proc != nil && p.proc.IsRunning() {
			return true
		}
	}
	return false
}

// Ensure starts an instance, or moves a running one to the wanted state.
func (m *Manager) Ensure(inst Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked(inst)
}

func (m *Manager) ensureLocked(inst Instance) error {
	if err := Available(); err != nil {
		return err
	}
	if err := ensureRuntimeDir(); err != nil {
		return err
	}

	newStructFP := inst.structuralFingerprint()
	newUsersFP := usersFingerprint(inst.Users)
	cur, had := m.procs[inst.Id]
	running := had && cur.proc != nil && cur.proc.IsRunning()

	switch actionFor(running, fpOf(cur, had, true), fpOf(cur, had, false), newStructFP, newUsersFP) {
	case actionNone:
		return nil
	case actionUsers:
		if err := writeUsers(inst, ResolveDropAccount()); err != nil {
			return err
		}
		cur.usersFP = newUsersFP
		cur.loginToEmail = inst.LoginToEmail()
		cur.limits = inst.Limits()
		return nil
	}

	if had {
		logger.Infof("openvpn: restarting inbound %d (was running: %v, settings changed: %v)",
			inst.Id, running, cur.structuralFP != newStructFP)
		if err := cur.proc.Stop(); err != nil && running {
			logger.Warningf("openvpn: inbound %d did not stop: %v", inst.Id, err)
		}
		delete(m.procs, inst.Id)
	}

	caFile, certFile, keyFile, err := ensureKeypair(inst)
	if err != nil {
		return err
	}
	if _, err := ensureTLSCryptKey(inst.Id); err != nil {
		return err
	}
	_ = caFile
	_ = certFile
	_ = keyFile

	drop := ResolveDropAccount()
	if err := writeUsers(inst, drop); err != nil {
		return err
	}

	helper, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the panel's own binary, which checks the passwords: %w", err)
	}
	cfg := renderConfig(inst, HelperCommand(helper, inst.Id), drop)
	if err := writeFile(configPathForID(inst.Id), []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("write the config: %w", err)
	}
	// A server the manager lost holds the tun device, and every start fails busy.
	stopStrays(configPathForID(inst.Id))
	// A socket left by a killed process blocks the new bind.
	_ = os.Remove(managementSocketForID(inst.Id))

	table := routeTableFor(inst.Id)
	ensureExit(inst.CIDR(), inst.Exit, table)

	bin, err := BinaryPath()
	if err != nil {
		return err
	}
	proc := sidecar.New(bin, "openvpn: "+inst.Tag, "--config", configPathForID(inst.Id))
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start openvpn for inbound %d: %w", inst.Id, err)
	}
	m.procs[inst.Id] = &managed{
		proc:         proc,
		tag:          inst.Tag,
		cidr:         inst.CIDR(),
		table:        table,
		exit:         inst.Exit,
		structuralFP: newStructFP,
		usersFP:      newUsersFP,
		loginToEmail: inst.LoginToEmail(),
		limits:       inst.Limits(),
		last:         map[string]counters{},
	}
	logger.Infof("openvpn: started inbound %d (%s) on port %d, pool %s", inst.Id, inst.Tag, inst.Port, inst.CIDR())
	return nil
}

func fpOf(cur *managed, had, structural bool) string {
	if !had || cur == nil {
		return ""
	}
	if structural {
		return cur.structuralFP
	}
	return cur.usersFP
}

func writeUsers(inst Instance, drop *DropAccount) error {
	body, problems := renderPasswd(inst.Users)
	for _, why := range problems {
		logger.Warningf("openvpn: inbound %d (%s): %s", inst.Id, inst.Tag, why)
	}
	// 0640 rather than 0644: the hashes are bcrypt and not worth much to a
	// reader, but there is no reason for every account on the host to have
	// them.
	//
	// The group is the one openvpn drops to, because the helper that reads
	// this file is spawned by openvpn after that drop and reads it as that
	// user. Root-only here means every customer is rejected on a correct
	// password, which is how this was found.
	gid := -1
	if drop != nil {
		gid = drop.GID
	}
	return writeFileOwnedBy(passwdPathForID(inst.Id), []byte(body), 0o640, gid)
}

// writeFile writes through a temporary file in the same directory, so a
// reader never sees half a file and a crash mid-write cannot leave the
// credentials truncated — which would lock every customer out at once.
func writeFile(path string, body []byte, mode os.FileMode) error {
	return writeFileOwnedBy(path, body, mode, -1)
}

// writeFileOwnedBy is writeFile with a group. A gid below zero leaves the
// ownership alone, which is what every caller but the credentials file wants.
//
// The chown happens before the rename, so the file is never visible at its
// real path with the wrong group: a reader either sees the previous file or
// the finished one.
func writeFileOwnedBy(path string, body []byte, mode os.FileMode, gid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if gid >= 0 {
		if err := os.Chown(tmp, -1, gid); err != nil {
			// Not fatal on its own, but it is the failure that locks every
			// customer out of this inbound, so it is said plainly rather
			// than left for someone to infer from "wrong password".
			logger.Warningf("openvpn: could not give %s to group %d; the password helper runs as that group and will reject every login: %v", path, gid, err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Remove stops an inbound and drops its generated state, keeping the
// certificate: an inbound disabled and enabled again must come back as the
// same server, or every client that pinned it refuses to connect.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
}

// Forget is Remove plus the keys, for an inbound that is actually gone.
func (m *Manager) Forget(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
	for _, p := range []string{
		caCertPathForID(id), caCertPathForID(id) + ".key",
		certPathForID(id), keyPathForID(id), tlsAuthPathForID(id),
	} {
		_ = os.Remove(p)
	}
}

func (m *Manager) removeLocked(id int) {
	cur, ok := m.procs[id]
	if !ok {
		return
	}
	_ = cur.proc.Stop()
	delete(m.procs, id)
	removeExit(cur.cidr, cur.table)
	removeNAT(cur.cidr)
	for _, p := range []string{
		configPathForID(id), passwdPathForID(id),
		managementSocketForID(id), statusPathForID(id),
	} {
		_ = os.Remove(p)
	}
	logger.Infof("openvpn: stopped inbound %d", id)
}

// Reconcile drives the running set toward the wanted instances.
func (m *Manager) Reconcile(desired []Instance) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make(map[int]struct{}, len(desired))
	for _, inst := range desired {
		wanted[inst.Id] = struct{}{}
		if err := m.ensureLocked(inst); err != nil {
			logger.Warningf("openvpn: inbound %d (%s): %v", inst.Id, inst.Tag, err)
		}
	}
	for id := range m.procs {
		if _, keep := wanted[id]; !keep {
			m.removeLocked(id)
		}
	}
}

// StopAll takes every sidecar down, for a panel that is shutting down.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.procs {
		m.removeLocked(id)
	}
}

// Result is what to show beside an inbound that is not running.
func (m *Manager) Result(id int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.procs[id]
	if !ok || cur.proc == nil {
		return ""
	}
	return cur.proc.Result()
}

// Traffic is one client's movement since the last time it was collected.
// Tag names the inbound it moved through, so the panel can total an inbound
// as well as a customer.
type Traffic struct {
	Email string
	Tag   string
	Up    int64
	Down  int64
}

// CollectTraffic folds what every sidecar reports into per-client deltas, and
// disconnects whatever is past its device limit while it is looking.
//
// Deltas are keyed by the process's own client id. A reconnect gets a new id
// and starts at zero, so keying by customer would read that as traffic going
// backwards and quietly lose everything they had used.
// It also reports who is connected, which is what the panel's online list is
// built from: a customer with no traffic in the last few seconds is still
// online, and leaving them out would make the list flicker.
func (m *Manager) CollectTraffic() ([]Traffic, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	byKey := map[string]*Traffic{}
	online := map[string]struct{}{}
	for id, cur := range m.procs {
		sessions := Sessions(id)
		seen := make(map[string]struct{}, len(sessions))
		for _, s := range sessions {
			seen[s.ClientID] = struct{}{}
			email := cur.loginToEmail[s.Login]
			if email == "" {
				email = s.Login
			}
			online[email] = struct{}{}
			was := cur.last[s.ClientID]
			up := s.BytesReceived - was.received
			down := s.BytesSent - was.sent
			cur.last[s.ClientID] = counters{received: s.BytesReceived, sent: s.BytesSent}
			if up < 0 || down < 0 {
				// The counters only go backwards when the id has been reused
				// for a different connection. Start again rather than bill a
				// negative number against somebody.
				continue
			}
			if up == 0 && down == 0 {
				continue
			}
			key := cur.tag + "\x00" + email
			t, ok := byKey[key]
			if !ok {
				t = &Traffic{Email: email, Tag: cur.tag}
				byKey[key] = t
			}
			t.Up += up
			t.Down += down
		}
		// A session that has gone takes its counter with it, or the map grows
		// for the lifetime of the panel.
		for clientID := range cur.last {
			if _, still := seen[clientID]; !still {
				delete(cur.last, clientID)
			}
		}

		for _, over := range OverLimit(sessions, func(login string) int { return cur.limits[login] }) {
			if err := Disconnect(id, over.ClientID); err != nil {
				logger.Warningf("openvpn: could not disconnect %s past their device limit: %v", over.Login, err)
				continue
			}
			logger.Infof("openvpn: %s was past their device limit; their oldest connection was disconnected", over.Login)
		}
	}

	out := make([]Traffic, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, *t)
	}
	emails := make([]string, 0, len(online))
	for email := range online {
		emails = append(emails, email)
	}
	return out, emails
}

// ActiveTags names the inbounds this manager is serving, which the panel
// needs so it can tell "nobody is online here" from "this inbound is not
// running at all".
func (m *Manager) ActiveTags() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.procs))
	for _, cur := range m.procs {
		out = append(out, cur.tag)
	}
	return out
}
