package ocserv

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Traffic is a per-client byte delta for one poll, shaped like the mtproto
// sidecar's so the job layer folds both into the usual accounting.
type Traffic struct {
	Tag   string
	Email string
	Up    int64
	Down  int64
}

type sessionCounters struct {
	rx int64
	tx int64
}

type managed struct {
	proc         *Process
	tag          string
	cidr         string
	structuralFP string
	usersFP      string
	ctlSocket    string
	// loginToEmail turns what occtl reports back into the panel identity
	// traffic is billed against; the two differ for every reseller client.
	loginToEmail map[string]string
	// last is keyed by occtl session id, not by user. A reconnect opens a new
	// session whose counters restart at zero; keying by user would read that
	// reset as a negative delta and silently drop the traffic.
	last map[int64]sessionCounters
}

// Manager owns the running ocserv processes, keyed by inbound id.
type Manager struct {
	mu    sync.Mutex
	procs map[int]*managed
	swept bool
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the process-wide ocserv manager singleton.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = &Manager{procs: map[int]*managed{}} })
	return manager
}

// ensureAction is what ensureLocked must do to move a running instance to the
// desired state.
type ensureAction int

const (
	ensureNoop ensureAction = iota
	// ensureUsers rewrites the password file in place. ocserv consults that
	// file on every authentication, so adding, removing or re-keying a client
	// needs no restart and drops nobody who is already online.
	ensureUsers
	ensureRestart
)

// ensureActionFor decides how to apply a desired instance. Anything ocserv
// only reads at startup forces a restart; a user-only change does not.
func ensureActionFor(running bool, curStructFP, curUsersFP, newStructFP, newUsersFP string) ensureAction {
	if !running || curStructFP != newStructFP {
		return ensureRestart
	}
	if curUsersFP != newUsersFP {
		return ensureUsers
	}
	return ensureNoop
}

// Ensure starts an instance, or moves a running one to the desired state.
func (m *Manager) Ensure(inst Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepOrphansLocked()
	return m.ensureLocked(inst)
}

func (m *Manager) sweepOrphansLocked() {
	if m.swept {
		return
	}
	m.swept = true
	if n := killStrayOcservProcesses(runtimeDir()); n > 0 {
		logger.Warningf("ocserv: terminated %d process(es) left over from a previous run", n)
	}
}

// writePerUserLimits writes one ocserv config file per client that has a
// device limit of its own, and removes the files of those that no longer do.
//
// Removing matters as much as writing: a file left behind keeps capping a
// client whose limit the operator has just cleared, and nothing in the panel
// would show why. ocserv reads these at authentication time, so both the new
// limit and its removal apply on the next login without a restart.
func writePerUserLimits(inst Instance) error {
	dir := perUserDirForID(inst.Id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("prepare %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}

	wanted := make(map[string]struct{}, len(inst.Users))
	for _, u := range inst.Users {
		if u.DeviceLimit <= 0 || ValidLogin(u.Login) != nil {
			continue
		}
		wanted[u.Login] = struct{}{}
		body := fmt.Sprintf("max-same-clients = %d\n", u.DeviceLimit)
		if err := writeFile(filepath.Join(dir, u.Login), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write device limit for %q: %w", u.Name, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, keep := wanted[e.Name()]; !keep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// writeUsers renders the password file and disconnects anyone the instance no
// longer serves. Removing a client from the file only blocks their next login:
// a session already established keeps running until it is explicitly cut.
func (m *Manager) writeUsers(inst Instance, ctlSocket string, kick bool) error {
	content, problems := renderPasswd(inst.Users)
	for _, p := range problems {
		logger.Warningf("ocserv: inbound %d: %s", inst.Id, p)
	}
	if err := writeFile(passwdPathForID(inst.Id), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write password file: %w", err)
	}
	if err := writePerUserLimits(inst); err != nil {
		return err
	}
	if !kick {
		return nil
	}
	// occtl speaks login names, so the set compared against it is logins.
	wanted := make(map[string]struct{}, len(inst.Users))
	for _, u := range inst.Users {
		wanted[u.Login] = struct{}{}
	}
	sessions, err := listSessions(ctlSocket)
	if err != nil {
		// A control socket that is not answering yet is normal right after a
		// start; the next reconcile kicks whoever is still there.
		return nil
	}
	kicked := map[string]struct{}{}
	for _, s := range sessions {
		if _, ok := wanted[s.Username]; ok {
			continue
		}
		if _, done := kicked[s.Username]; done {
			continue
		}
		kicked[s.Username] = struct{}{}
		if err := disconnectUser(ctlSocket, s.Username); err != nil {
			logger.Warningf("ocserv: could not disconnect %q from inbound %d: %v", s.Username, inst.Id, err)
			continue
		}
		logger.Infof("ocserv: disconnected %q from inbound %d (no longer served)", s.Username, inst.Id)
	}
	return nil
}

func (m *Manager) ensureLocked(inst Instance) error {
	if err := Available(); err != nil {
		return err
	}
	if err := ensureRuntimeDir(); err != nil {
		return fmt.Errorf("prepare %s: %w", runtimeDir(), err)
	}

	certFile, keyFile, err := ensureKeypair(inst)
	if err != nil {
		return err
	}

	newStructFP := inst.structuralFingerprint()
	newUsersFP := inst.usersFingerprint()
	cur, had := m.procs[inst.Id]
	running := had && cur.proc != nil && cur.proc.IsRunning()

	switch ensureActionFor(running, fpOf(cur, had, true), fpOf(cur, had, false), newStructFP, newUsersFP) {
	case ensureNoop:
		return nil
	case ensureUsers:
		if err := m.writeUsers(inst, cur.ctlSocket, true); err != nil {
			return err
		}
		cur.usersFP = newUsersFP
		cur.loginToEmail = inst.LoginToEmail()
		return nil
	}

	if had {
		_ = cur.proc.Stop()
		delete(m.procs, inst.Id)
	}

	if err := m.writeUsers(inst, ctlSocketForID(inst.Id), false); err != nil {
		return err
	}
	cfg := renderConfig(inst, certFile, keyFile)
	if err := writeFile(configPathForID(inst.Id), []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// A socket left behind by a killed process blocks the new bind.
	_ = os.Remove(ctlSocketForID(inst.Id))

	ensureNAT(inst.CIDR())

	proc := newProcess(configPathForID(inst.Id), inst.Tag)
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start ocserv for inbound %d: %w", inst.Id, err)
	}
	m.procs[inst.Id] = &managed{
		proc:         proc,
		tag:          inst.Tag,
		cidr:         inst.CIDR(),
		structuralFP: newStructFP,
		usersFP:      newUsersFP,
		ctlSocket:    ctlSocketForID(inst.Id),
		loginToEmail: inst.LoginToEmail(),
		last:         map[int64]sessionCounters{},
	}
	logger.Infof("ocserv: started inbound %d (%s) on port %d, pool %s", inst.Id, inst.Tag, inst.Port, inst.CIDR())
	return nil
}

// fpOf reads a fingerprint off a possibly-absent managed entry.
func fpOf(cur *managed, had, structural bool) string {
	if !had || cur == nil {
		return ""
	}
	if structural {
		return cur.structuralFP
	}
	return cur.usersFP
}

// Remove stops the instance for an inbound id and drops its generated state,
// but keeps its TLS keypair. An inbound that is disabled and later enabled
// again must come back with the same certificate: a new one would look like a
// different server to every client that pinned the old one, and to the
// customer that reads as the VPN having broken.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
}

// Forget is Remove plus the keypair, for an inbound that is actually gone.
// Keeping a private key on disk for an inbound nobody can reach again is key
// material with no owner.
func (m *Manager) Forget(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
	for _, p := range []string{certPathForID(id), keyPathForID(id)} {
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
	removeNAT(cur.cidr)
	for _, p := range []string{configPathForID(id), passwdPathForID(id), ctlSocketForID(id)} {
		_ = os.Remove(p)
	}
	_ = os.RemoveAll(perUserDirForID(id))
	logger.Infof("ocserv: stopped inbound %d", id)
}

// Reconcile drives the running set toward the desired instances.
func (m *Manager) Reconcile(desired []Instance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepOrphansLocked()
	want := make(map[int]struct{}, len(desired))
	for _, inst := range desired {
		want[inst.Id] = struct{}{}
	}
	for id := range maps.Clone(m.procs) {
		if _, ok := want[id]; !ok {
			m.removeLocked(id)
		}
	}
	for _, inst := range desired {
		if err := m.ensureLocked(inst); err != nil {
			logger.Warningf("ocserv: inbound %d could not be served: %v", inst.Id, err)
		}
	}
}

// StopAll stops every managed instance. Called on panel shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range maps.Clone(m.procs) {
		m.removeLocked(id)
	}
}

// HasRunning reports whether any cisco inbound is currently being served.
func (m *Manager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.procs {
		if cur.proc != nil && cur.proc.IsRunning() {
			return true
		}
	}
	return false
}

// Result returns the last thing the ocserv process for an inbound said, for
// surfacing a start failure in the UI.
func (m *Manager) Result(id int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.procs[id]; ok && cur.proc != nil {
		return cur.proc.GetResult()
	}
	return ""
}

// CollectTraffic reads every running instance's live sessions and returns the
// per-client byte deltas since the previous poll, plus the clients with at
// least one connected session.
func (m *Manager) CollectTraffic() ([]Traffic, []string) {
	type snap struct {
		id      int
		tag     string
		socket  string
		toEmail map[string]string
		last    map[int64]sessionCounters
	}
	m.mu.Lock()
	snaps := make([]snap, 0, len(m.procs))
	for id, cur := range m.procs {
		if cur.proc == nil || !cur.proc.IsRunning() {
			continue
		}
		snaps = append(snaps, snap{
			id:      id,
			tag:     cur.tag,
			socket:  cur.ctlSocket,
			toEmail: maps.Clone(cur.loginToEmail),
			last:    maps.Clone(cur.last),
		})
	}
	m.mu.Unlock()

	var out []Traffic
	onlineSet := map[string]struct{}{}
	for _, s := range snaps {
		sessions, err := listSessions(s.socket)
		if err != nil {
			continue
		}
		deltas, newLast := foldSessions(sessions, s.last)
		for login, c := range deltas {
			out = append(out, Traffic{Tag: s.tag, Email: emailFor(s.toEmail, login), Up: c.rx, Down: c.tx})
		}
		for _, sess := range sessions {
			if sess.State == "connected" {
				onlineSet[emailFor(s.toEmail, sess.Username)] = struct{}{}
			}
		}
		m.mu.Lock()
		if cur, ok := m.procs[s.id]; ok {
			cur.last = newLast
		}
		m.mu.Unlock()
	}
	return out, slices.Sorted(maps.Keys(onlineSet))
}

// foldSessions turns per-session cumulative counters into per-user deltas.
//
// Each session's counters start at zero and only grow, so a session seen
// before contributes the growth since the last poll and a session seen for the
// first time contributes its whole total. Keying the previous values by user
// instead of by session would turn every reconnect into a negative delta, and
// a user with two devices into whichever device polled last.
func foldSessions(sessions []Session, last map[int64]sessionCounters) (map[string]sessionCounters, map[int64]sessionCounters) {
	deltas := map[string]sessionCounters{}
	newLast := make(map[int64]sessionCounters, len(sessions))
	for _, s := range sessions {
		newLast[s.ID] = sessionCounters{rx: s.RX, tx: s.TX}
		rx, tx := s.RX, s.TX
		if prev, seen := last[s.ID]; seen {
			rx, tx = s.RX-prev.rx, s.TX-prev.tx
		}
		// A counter that went backwards means the session id was reused after
		// a restart; counting the whole total again would inflate the user.
		if rx < 0 {
			rx = 0
		}
		if tx < 0 {
			tx = 0
		}
		if rx == 0 && tx == 0 {
			continue
		}
		cur := deltas[s.Username]
		deltas[s.Username] = sessionCounters{rx: cur.rx + rx, tx: cur.tx + tx}
	}
	return deltas, newLast
}

// emailFor translates a login name occtl reported back into the client email.
// An unmapped login is handed back as-is rather than dropped: traffic from a
// session that outlived a config change still belongs to somebody, and losing
// it silently is worse than attributing it to a name the panel can show.
func emailFor(toEmail map[string]string, login string) string {
	if email, ok := toEmail[login]; ok {
		return email
	}
	return login
}
