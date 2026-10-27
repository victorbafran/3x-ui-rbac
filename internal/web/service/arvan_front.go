package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/arvan"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"

	"gorm.io/gorm"
)

// A front on ArvanCloud keeps one active IP, the relay's Address, and up to three
// tested reserves; a swap moves the box's routes, the DNS record and the tunnel.

// Reserve IP status values, as stored and as the UI reads them.
const (
	ArvanIPTesting = "testing"
	ArvanIPHealthy = "healthy"
	ArvanIPDead    = "dead"
)

const (
	arvanMaxReserves   = 3
	arvanMaxIPs        = 4 // Arvan's own cap per server, the active IP included
	arvanMaxAddsPerDay = 12
	arvanMaxDeadTries  = 3
	arvanSwapAfter     = 3 // failed checks a minute apart before an auto swap
	arvanActionTimeout = 10 * time.Minute
	arvanCallTimeout   = 2 * time.Minute
	arvanHotplugWait   = 8 * time.Second
	arvanTimerWait     = 15 * time.Second // the front's routing timer runs every 10 s
	arvanSSHHandshake  = time.Minute
	arvanSweepEvery    = 15 * time.Minute
	arvanAutoEvery     = 5 * time.Minute
	arvanDailyWindow   = time.Hour
	arvanRecordTTL     = 5 * time.Minute
	arvanSealPrefix    = "aes256gcm:v1:"
	arvanReadyMark     = "x-ui-multi-ip ready"
)

// arvanKeyFile seals the API key, so a database-only backup carries ciphertext.
var arvanKeyFile = func() string { return filepath.Join(config.GetDBFolderPath(), "arvan.key") }

var arvanKeyAAD = []byte("settings/arvanApiKey")

type ArvanService struct {
	settings SettingService
	relays   RelayService
}

// ArvanFrontRequest is the editable part of a front's Arvan binding.
type ArvanFrontRequest struct {
	Domain        string `json:"domain"`
	RecordName    string `json:"recordName"`
	ReserveTarget int    `json:"reserveTarget"`
	AutoSwap      bool   `json:"autoSwap"`
	DailySwapAt   string `json:"dailySwapAt"`
}

// ArvanKeyState is whether a key is stored and whether this panel can open it.
type ArvanKeyState struct {
	Configured bool `json:"configured"`
	Readable   bool `json:"readable"`
}

type ArvanReserveView struct {
	IP        string `json:"ip"`
	Status    string `json:"status"`
	LastCheck int64  `json:"lastCheck"`
	LastError string `json:"lastError"`
	IranOk    int    `json:"iranOk"`
	IranTotal int    `json:"iranTotal"`
	CreatedAt int64  `json:"createdAt"`
}

// ArvanCandidate is an A record that points at the front or carries its public name.
type ArvanCandidate struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	IP     string `json:"ip"`
	id     string
}

type ArvanFrontView struct {
	RelayId       int                `json:"relayId"`
	RelayName     string             `json:"relayName"`
	KeyConfigured bool               `json:"keyConfigured"`
	KeyReadable   bool               `json:"keyReadable"`
	Bound         bool               `json:"bound"`
	Zone          string             `json:"zone"`
	ServerId      string             `json:"serverId"`
	ServerName    string             `json:"serverName"`
	Domain        string             `json:"domain"`
	RecordName    string             `json:"recordName"`
	RecordId      string             `json:"recordId"`
	RecordIp      string             `json:"recordIp"`
	ActiveIp      string             `json:"activeIp"`
	ActiveHealthy bool               `json:"activeHealthy"`
	ActiveFails   int                `json:"activeFails"`
	ReserveTarget int                `json:"reserveTarget"`
	AutoSwap      bool               `json:"autoSwap"`
	DailySwapAt   string             `json:"dailySwapAt"`
	Prepared      bool               `json:"prepared"`
	Busy          string             `json:"busy"`
	LastAction    string             `json:"lastAction"`
	LastError     string             `json:"lastError"`
	LastSwap      int64              `json:"lastSwap"`
	LastCheck     int64              `json:"lastCheck"`
	Reserves      []ArvanReserveView `json:"reserves"`
	Candidates    []ArvanCandidate   `json:"candidates"`
	MaxReserves   int                `json:"maxReserves"`
	AddsToday     int                `json:"addsToday"`
}

// arvanState is a front's in-memory side: the action lock, the running
// action's label, and what is cached from Arvan between polls.
type arvanState struct {
	lock sync.Mutex

	mu         sync.Mutex
	busy       string
	recordIP   string
	recordAt   time.Time
	candidates []ArvanCandidate
	lastSweep  time.Time
	lastAuto   time.Time
}

var arvanStates sync.Map // relay id -> *arvanState

func arvanStateOf(id int) *arvanState {
	st, _ := arvanStates.LoadOrStore(id, &arvanState{})
	return st.(*arvanState)
}

// claim marks the front busy with label, or says what it is busy with.
func (st *arvanState) claim(label string) (string, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busy != "" {
		return st.busy, false
	}
	st.busy = label
	return label, true
}

func (st *arvanState) done() {
	st.mu.Lock()
	st.busy = ""
	st.mu.Unlock()
}

func (st *arvanState) busyLabel() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.busy
}

func (st *arvanState) setRecordIP(ip string) {
	st.mu.Lock()
	st.recordIP, st.recordAt = ip, time.Now()
	st.mu.Unlock()
}

// due reports whether every has passed since *last, and restarts the clock if so.
func (st *arvanState) due(last *time.Time, every time.Duration, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !last.IsZero() && now.Sub(*last) < every {
		return false
	}
	*last = now
	return true
}

func errArvanBusy(label string) error {
	return fmt.Errorf("the front is busy (%s); try again when it finishes", label)
}

// KeyStatus says whether an Arvan key is stored and whether it can be opened.
func (s *ArvanService) KeyStatus() (configured, readable bool) {
	stored, err := s.settings.GetArvanApiKey()
	if err != nil || stored == "" {
		return false, false
	}
	_, err = openArvanKey(stored)
	return true, err == nil
}

// SetKey stores a key only after Arvan accepts it.
func (s *ArvanService) SetKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("paste the Arvan API key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	if _, err := arvan.New(key, nil).Zones(ctx); err != nil {
		return fmt.Errorf("the key was refused: %w", err)
	}
	sealed, err := sealArvanKey(key)
	if err != nil {
		return err
	}
	return s.settings.SetArvanApiKey(sealed)
}

func (s *ArvanService) ClearKey() error {
	return s.settings.SetArvanApiKey("")
}

func (s *ArvanService) client() (*arvan.Client, error) {
	stored, err := s.settings.GetArvanApiKey()
	if err != nil {
		return nil, err
	}
	if stored == "" {
		return nil, errors.New("no Arvan API key is set")
	}
	key, err := openArvanKey(stored)
	if err != nil {
		return nil, err
	}
	return arvan.New(key, nil), nil
}

// arvanSealKey reads the sealing key, making it on first use when create is set.
func arvanSealKey(create bool) ([]byte, error) {
	path := arvanKeyFile()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return arvanSealKey(false)
		}
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(path)
			return nil, werr
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the Arvan key file is unreadable; enter the key again: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s is not a 32-byte key; enter the Arvan key again", path)
	}
	return key, nil
}

func arvanGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealArvanKey(plain string) (string, error) {
	key, err := arvanSealKey(true)
	if err != nil {
		return "", err
	}
	gcm, err := arvanGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), arvanKeyAAD)
	return arvanSealPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openArvanKey(stored string) (string, error) {
	if !strings.HasPrefix(stored, arvanSealPrefix) {
		return "", errors.New("the stored Arvan key is not sealed; enter it again")
	}
	key, err := arvanSealKey(false)
	if err != nil {
		return "", err
	}
	blob, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, arvanSealPrefix))
	if err != nil {
		return "", errors.New("the stored Arvan key is damaged; enter it again")
	}
	gcm, err := arvanGCM(key)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", errors.New("the stored Arvan key is damaged; enter it again")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], arvanKeyAAD)
	if err != nil {
		return "", errors.New("the Arvan key file does not open the stored key; enter it again")
	}
	return string(plain), nil
}

func arvanRelay(id int) (model.Relay, error) {
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		return r, err
	}
	if r.Role != model.RelayFront {
		return r, fmt.Errorf("%s is not an entry server", r.Name)
	}
	return r, nil
}

// arvanFront is the front's binding row; a front never detected gets a blank one.
func arvanFront(relayId int) (model.ArvanFront, error) {
	var f model.ArvanFront
	err := database.GetDB().Where("relay_id = ?", relayId).First(&f).Error
	if database.IsNotFound(err) {
		return model.ArvanFront{RelayId: relayId}, nil
	}
	return f, err
}

func ensureArvanFront(relayId int) error {
	f := model.ArvanFront{RelayId: relayId}
	return database.GetDB().Where("relay_id = ?", relayId).FirstOrCreate(&f).Error
}

func updateArvanFront(relayId int, fields map[string]any) error {
	return database.GetDB().Model(&model.ArvanFront{}).Where("relay_id = ?", relayId).Updates(fields).Error
}

func arvanReserves(relayId int) ([]model.ArvanIP, error) {
	var rows []model.ArvanIP
	err := database.GetDB().Where("relay_id = ?", relayId).Order("created_at asc, id asc").Find(&rows).Error
	return rows, err
}

// liveReserves counts the reserves that still count toward the target.
func liveReserves(rows []model.ArvanIP) int {
	n := 0
	for _, row := range rows {
		if row.Status == ArvanIPHealthy || row.Status == ArvanIPTesting {
			n++
		}
	}
	return n
}

// strayPorts are the public IPv4 ports on the front's Arvan server that carry
// neither the active IP nor a tracked reserve.
func strayPorts(ctx context.Context, c *arvan.Client, r model.Relay, f model.ArvanFront) ([]arvan.Port, error) {
	ports, err := c.Ports(ctx, f.Zone)
	if err != nil {
		return nil, err
	}
	rows, err := arvanReserves(r.Id)
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{r.Address: true}
	for _, row := range rows {
		tracked[row.IP] = true
	}
	var out []arvan.Port
	for _, p := range ports {
		if p.InstanceID != f.ServerId || len(p.IPs) == 0 || slices.ContainsFunc(p.IPs, func(ip string) bool { return tracked[ip] }) {
			continue
		}
		if slices.ContainsFunc(p.IPs, func(ip string) bool { v := net.ParseIP(ip); return v != nil && v.To4() != nil }) {
			out = append(out, p)
		}
	}
	return out, nil
}

// waitStrayPort looks, for half a minute, for the IP an add whose reply was
// lost made anyway.
func waitStrayPort(ctx context.Context, c *arvan.Client, r model.Relay, f model.ArvanFront) (arvan.Port, bool) {
	for range 6 {
		select {
		case <-ctx.Done():
			return arvan.Port{}, false
		case <-time.After(5 * time.Second):
		}
		if strays, err := strayPorts(ctx, c, r, f); err == nil && len(strays) > 0 {
			return strays[0], true
		}
	}
	return arvan.Port{}, false
}

// settleStrays keeps untracked IPs as reserves to test while the front is
// short of its target, and gives the rest back, so no paid IP is forgotten.
func (s *ArvanService) settleStrays(ctx context.Context, c *arvan.Client, r model.Relay, f model.ArvanFront) (string, error) {
	strays, err := strayPorts(ctx, c, r, f)
	if err != nil || len(strays) == 0 {
		return "", err
	}
	rows, err := arvanReserves(r.Id)
	if err != nil {
		return "", err
	}
	live := liveReserves(rows)
	var notes []string
	for _, p := range strays {
		ip := firstOf(p.IPs)
		if live < f.ReserveTarget {
			if err := database.GetDB().Create(&model.ArvanIP{RelayId: r.Id, IP: ip, PortId: p.ID, Status: ArvanIPTesting}).Error; err == nil {
				live++
				notes = append(notes, "kept the untracked "+ip+" as a reserve to test")
			}
			continue
		}
		if err := releasePort(ctx, c, r.Id, f.Zone, p); err != nil {
			logger.Warningf("front %s: could not release the untracked IP %s: %v", r.Name, ip, err)
			continue
		}
		logger.Infof("front %s: released %s, an IP on its Arvan server the panel had lost track of", r.Name, ip)
		notes = append(notes, "released the untracked "+ip)
	}
	return strings.Join(notes, "; "), nil
}

// deleteArvanRows forgets a deleted front's binding and reserves; the IPs stay
// on the Arvan account, since releasing them is never a side effect.
func deleteArvanRows(tx *gorm.DB, relayId int) error {
	if err := tx.Where("relay_id = ?", relayId).Delete(&model.ArvanIP{}).Error; err != nil {
		return err
	}
	if err := tx.Where("relay_id = ?", relayId).Delete(&model.ArvanFront{}).Error; err != nil {
		return err
	}
	arvanStates.Delete(relayId)
	return nil
}

func (s *ArvanService) location() *time.Location {
	if loc, err := s.settings.GetTimeLocation(); err == nil && loc != nil {
		return loc
	}
	return time.Local
}

func addsToday(f model.ArvanFront, day string) int {
	if f.AddsDay == day {
		return f.AddsCount
	}
	return 0
}

// View is the front's Arvan state as the panel shows it; the key never appears.
func (s *ArvanService) View(relayId int) (ArvanFrontView, error) {
	r, err := arvanRelay(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	f, err := arvanFront(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	rows, err := arvanReserves(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	configured, readable := s.KeyStatus()
	st := arvanStateOf(relayId)
	v := ArvanFrontView{
		RelayId: r.Id, RelayName: r.Name, KeyConfigured: configured, KeyReadable: readable,
		Bound: f.ServerId != "", Zone: f.Zone, ServerId: f.ServerId, ServerName: f.ServerName,
		Domain: f.Domain, RecordName: f.RecordName, RecordId: f.RecordId,
		ActiveIp: r.Address, ActiveHealthy: f.LastCheck > 0 && f.ActiveFails == 0, ActiveFails: f.ActiveFails,
		ReserveTarget: f.ReserveTarget, AutoSwap: f.AutoSwap, DailySwapAt: f.DailySwapAt, Prepared: f.Prepared,
		Busy: st.busyLabel(), LastAction: f.LastAction, LastError: f.LastError, LastSwap: f.LastSwap, LastCheck: f.LastCheck,
		Reserves:    make([]ArvanReserveView, 0, len(rows)),
		MaxReserves: arvanMaxReserves,
		AddsToday:   addsToday(f, time.Now().In(s.location()).Format(time.DateOnly)),
	}
	for _, row := range rows {
		if row.IP == r.Address {
			continue
		}
		v.Reserves = append(v.Reserves, ArvanReserveView{
			IP: row.IP, Status: row.Status, LastCheck: row.LastCheck, LastError: row.LastError,
			IranOk: row.IranOk, IranTotal: row.IranTotal, CreatedAt: row.CreatedAt,
		})
	}
	st.mu.Lock()
	v.Candidates = append([]ArvanCandidate{}, st.candidates...)
	st.mu.Unlock()
	v.RecordIp = s.recordIP(st, f, readable)
	return v, nil
}

// recordIP is where the bound record points, read from Arvan at most every
// few minutes since the page polls this view.
func (s *ArvanService) recordIP(st *arvanState, f model.ArvanFront, readable bool) string {
	if f.Domain == "" || f.RecordId == "" {
		return ""
	}
	st.mu.Lock()
	ip, fresh := st.recordIP, time.Since(st.recordAt) < arvanRecordTTL
	if !fresh {
		st.recordAt = time.Now()
	}
	st.mu.Unlock()
	if fresh || !readable {
		return ip
	}
	c, err := s.client()
	if err != nil {
		return ip
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := arvanRecordByID(ctx, c, f.Domain, f.RecordId)
	if err != nil {
		return ip
	}
	got := firstOf(rec.IPs)
	st.setRecordIP(got)
	return got
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func arvanRecordByID(ctx context.Context, c *arvan.Client, domain, id string) (arvan.Record, error) {
	recs, err := c.ARecords(ctx, domain)
	if err != nil {
		return arvan.Record{}, err
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec, nil
		}
	}
	return arvan.Record{}, fmt.Errorf("the A record %s is no longer in %s; detect again", id, domain)
}

// recordFQDN is the name a record answers for; Arvan writes the apex as "@".
func recordFQDN(name, domain string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	switch {
	case name == "" || name == "@":
		return domain
	case name == domain || strings.HasSuffix(name, "."+domain):
		return name
	}
	return name + "." + domain
}

// Detect finds the Arvan server holding the front's address and the A records
// that point at it, binding the record when only one fits.
func (s *ArvanService) Detect(relayId int) (ArvanFrontView, error) {
	if global.IsStandby() {
		return ArvanFrontView{}, global.ErrStandby
	}
	r, err := arvanRelay(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	if net.ParseIP(r.Address) == nil {
		return ArvanFrontView{}, fmt.Errorf("%s is a name; an IP swap needs the front added by its IP", r.Address)
	}
	c, err := s.client()
	if err != nil {
		return ArvanFrontView{}, err
	}
	st := arvanStateOf(relayId)
	if busy, ok := st.claim("detect"); !ok {
		return ArvanFrontView{}, errArvanBusy(busy)
	}
	err = func() error {
		defer st.done()
		st.lock.Lock()
		defer st.lock.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
		defer cancel()

		server, err := c.FindServerByIP(ctx, r.Address)
		if errors.Is(err, arvan.ErrNotFound) {
			return fmt.Errorf("no server on this Arvan account holds %s: %w", r.Address, err)
		}
		if err != nil {
			return err
		}
		if err := ensureArvanFront(relayId); err != nil {
			return err
		}
		f, err := arvanFront(relayId)
		if err != nil {
			return err
		}
		fields := map[string]any{
			"zone": server.Zone, "server_id": server.ID, "server_name": server.Name,
			"last_action": "detect: " + server.Name + " in " + server.Zone, "last_error": "",
		}
		candidates, derr := arvanDNSCandidates(ctx, c, r)
		if derr != nil {
			fields["last_error"] = "dns: " + derr.Error()
		}
		if pick, ok := pickArvanCandidate(candidates, r.PublicName, f); ok {
			fields["domain"], fields["record_name"], fields["record_id"] = pick.Domain, pick.Name, pick.id
			st.setRecordIP(pick.IP)
			followPublicName(r, f, pick.Domain, pick.Name)
		}
		st.mu.Lock()
		st.candidates = candidates
		st.mu.Unlock()
		return updateArvanFront(relayId, fields)
	}()
	if err != nil {
		return ArvanFrontView{}, err
	}
	return s.View(relayId)
}

// arvanDNSCandidates is every A record pointing at the front's address, plus
// the one named by its public name wherever that points now.
func arvanDNSCandidates(ctx context.Context, c *arvan.Client, r model.Relay) ([]ArvanCandidate, error) {
	domains, err := c.Domains(ctx)
	if err != nil {
		return []ArvanCandidate{}, err
	}
	out := []ArvanCandidate{}
	var lastErr error
	for _, d := range domains {
		recs, err := c.ARecords(ctx, d)
		if err != nil {
			lastErr = err
			continue
		}
		for _, rec := range recs {
			named := r.PublicName != "" && recordFQDN(rec.Name, d) == r.PublicName
			if !named && !slices.Contains(rec.IPs, r.Address) {
				continue
			}
			out = append(out, ArvanCandidate{Domain: d, Name: rec.Name, IP: firstOf(rec.IPs), id: rec.ID})
		}
	}
	if len(out) == 0 && lastErr != nil {
		return out, lastErr
	}
	return out, nil
}

func pickArvanCandidate(cands []ArvanCandidate, publicName string, f model.ArvanFront) (ArvanCandidate, bool) {
	if publicName != "" {
		for _, c := range cands {
			if recordFQDN(c.Name, c.Domain) == publicName {
				return c, true
			}
		}
	}
	if len(cands) == 1 {
		return cands[0], true
	}
	for _, c := range cands {
		if c.Domain == f.Domain && c.Name == f.RecordName {
			return c, true
		}
	}
	return ArvanCandidate{}, false
}

func normalizeDailySwapAt(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	t, err := time.Parse("15:04", raw)
	if err != nil {
		return "", fmt.Errorf("the daily swap time %q is not HH:MM", raw)
	}
	return t.Format("15:04"), nil
}

// Save stores the operator's choices, resolving the record's id when its name moved.
func (s *ArvanService) Save(relayId int, req ArvanFrontRequest) (ArvanFrontView, error) {
	if global.IsStandby() {
		return ArvanFrontView{}, global.ErrStandby
	}
	if _, err := arvanRelay(relayId); err != nil {
		return ArvanFrontView{}, err
	}
	if req.ReserveTarget < 0 || req.ReserveTarget > arvanMaxReserves {
		return ArvanFrontView{}, fmt.Errorf("a front keeps 0 to %d reserve IPs, not %d", arvanMaxReserves, req.ReserveTarget)
	}
	daily, err := normalizeDailySwapAt(req.DailySwapAt)
	if err != nil {
		return ArvanFrontView{}, err
	}
	f, err := arvanFront(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	if f.ServerId == "" {
		return ArvanFrontView{}, errors.New("detect the Arvan server first")
	}
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Domain), "."))
	name := strings.ToLower(strings.TrimSpace(req.RecordName))
	if (domain == "") != (name == "") {
		return ArvanFrontView{}, errors.New("a DNS record needs both its domain and its name")
	}
	fields := map[string]any{
		"reserve_target": req.ReserveTarget, "auto_swap": req.AutoSwap, "daily_swap_at": daily,
		"domain": domain, "record_name": name,
	}
	switch {
	case domain == "":
		fields["record_id"] = ""
	case domain != f.Domain || name != f.RecordName || f.RecordId == "":
		c, err := s.client()
		if err != nil {
			return ArvanFrontView{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
		defer cancel()
		recs, err := c.ARecords(ctx, domain)
		if err != nil {
			return ArvanFrontView{}, err
		}
		i := slices.IndexFunc(recs, func(rec arvan.Record) bool { return strings.EqualFold(rec.Name, name) })
		if i < 0 {
			return ArvanFrontView{}, fmt.Errorf("%s has no A record named %s", domain, name)
		}
		fields["record_id"] = recs[i].ID
		arvanStateOf(relayId).setRecordIP(firstOf(recs[i].IPs))
	}
	if err := updateArvanFront(relayId, fields); err != nil {
		return ArvanFrontView{}, err
	}
	if domain != "" {
		if r, err := arvanRelay(relayId); err == nil {
			followPublicName(r, f, domain, name)
		}
	}
	return s.View(relayId)
}

// followPublicName makes the bound record the front's public name, so the
// inbound form offers the name swaps move; a name the operator typed stays.
func followPublicName(r model.Relay, was model.ArvanFront, domain, name string) {
	fqdn := recordFQDN(name, domain)
	prev := ""
	if was.Domain != "" && was.RecordName != "" {
		prev = recordFQDN(was.RecordName, was.Domain)
	}
	if r.PublicName == fqdn || (r.PublicName != "" && r.PublicName != prev) {
		return
	}
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ? AND public_name = ?", r.Id, r.PublicName).
		Update("public_name", fqdn).Error; err != nil {
		logger.Warningf("front %s: could not set its public name to %s: %v", r.Name, fqdn, err)
		return
	}
	logger.Infof("front %s: public name set to %s, the record its IP swaps move", r.Name, fqdn)
}

// start runs a long action behind the request, one per front at a time, and
// records how it ended on the front's row.
func (s *ArvanService) start(relayId int, label string, run func(ctx context.Context) (string, error)) error {
	if _, err := arvanRelay(relayId); err != nil {
		return err
	}
	st := arvanStateOf(relayId)
	if busy, ok := st.claim(label); !ok {
		return errArvanBusy(busy)
	}
	common.GoRecover("arvan-"+label, func() {
		defer st.done()
		st.lock.Lock()
		defer st.lock.Unlock()
		s.runAction(relayId, label, run)
	})
	return nil
}

func (s *ArvanService) runAction(relayId int, label string, run func(ctx context.Context) (string, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), arvanActionTimeout)
	defer cancel()
	detail, err := run(ctx)
	// a front deleted while this ran keeps no row
	r, rerr := arvanRelay(relayId)
	if rerr != nil {
		return
	}
	action := label
	if detail != "" {
		action += ": " + detail
	}
	fields := map[string]any{"last_action": action, "last_error": ""}
	if err != nil {
		fields["last_error"] = err.Error()
		logger.Warningf("front %s: arvan %s: %v", r.Name, label, err)
	}
	if ensureArvanFront(relayId) == nil {
		_ = updateArvanFront(relayId, fields)
	}
}

// runOnFront runs a script on the front over SSH to via, one of its IPs. The
// handshake has no deadline of its own, and a stalled one would hold the lock.
func runOnFront(ctx context.Context, r model.Relay, via, name, body string, args ...string) (string, error) {
	creds, err := frontCreds(r)
	if err != nil {
		return "", err
	}
	creds.Address = via
	type dialed struct {
		cl  *relay.Client
		err error
	}
	hctx, cancel := context.WithTimeout(ctx, arvanSSHHandshake)
	defer cancel()
	ch := make(chan dialed, 1)
	go func() {
		cl, err := relay.Dial(hctx, creds)
		ch <- dialed{cl, err}
	}()
	var cl *relay.Client
	select {
	case d := <-ch:
		if d.err != nil {
			return "", d.err
		}
		cl = d.cl
	case <-hctx.Done():
		go func() {
			if d := <-ch; d.cl != nil {
				d.cl.Close()
			}
		}()
		return "", fmt.Errorf("ssh to %s did not finish its handshake: %w", via, hctx.Err())
	}
	defer cl.Close()
	return cl.RunScript(ctx, name, body, args...)
}

// setPrimary gives primary the box's main default route; UDP replies from
// wildcard sockets leave by it, so it must be the active IP's.
func setPrimary(ctx context.Context, r model.Relay, via, primary string) error {
	out, err := runOnFront(ctx, r, via, "x-ui-multi-ip-primary.sh", arvan.SetPrimaryScript(), primary)
	if err != nil {
		return err
	}
	if !defaultRouteFrom(out, primary) {
		return fmt.Errorf("the front's default route did not move to %s: %s", primary, strings.TrimSpace(out))
	}
	return nil
}

// defaultRouteFrom reads `ip route show default` for a route whose src is ip.
func defaultRouteFrom(out, ip string) bool {
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "src" && fields[i+1] == ip {
				return true
			}
		}
	}
	return false
}

// Prepare installs per-IP routing on the front, so a new NIC never takes the
// active IP's replies.
func (s *ArvanService) Prepare(relayId int) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	return s.start(relayId, "prepare", func(ctx context.Context) (string, error) {
		r, err := arvanRelay(relayId)
		if err != nil {
			return "", err
		}
		out, err := runOnFront(ctx, r, r.Address, "x-ui-multi-ip-install.sh", arvan.InstallScript(), r.Address)
		if err != nil {
			return "", err
		}
		if !strings.Contains(out, arvanReadyMark) {
			return "", fmt.Errorf("the routing script did not finish: %s", strings.TrimSpace(out))
		}
		if err := ensureArvanFront(relayId); err != nil {
			return "", err
		}
		return "routing installed on " + r.Address, updateArvanFront(relayId, map[string]any{"prepared": true})
	})
}

// Refill adds tested reserves until the front holds its target.
func (s *ArvanService) Refill(relayId int) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	return s.start(relayId, "refill", func(ctx context.Context) (string, error) {
		r, err := arvanRelay(relayId)
		if err != nil {
			return "", err
		}
		f, err := arvanFront(relayId)
		if err != nil {
			return "", err
		}
		return s.refill(ctx, r, f.ReserveTarget, false)
	})
}

func publicIPv4Count(server arvan.Server) int {
	seen := map[string]bool{}
	for _, a := range server.Addresses {
		if ip := net.ParseIP(a.IP); a.Public && ip != nil && ip.To4() != nil {
			seen[a.IP] = true
		}
	}
	return len(seen)
}

// refill adds IPs until goal reserves are live, testing each from outside; a
// dead one is given back at once. Arvan's 4-IP cap and a daily cap bound it.
func (s *ArvanService) refill(ctx context.Context, r model.Relay, goal int, emergency bool) (string, error) {
	f, err := arvanFront(r.Id)
	if err != nil {
		return "", err
	}
	if f.ServerId == "" {
		return "", errors.New("detect the Arvan server first")
	}
	if !f.Prepared {
		return "", errors.New("prepare the server first; a new IP on an unprepared server can cut the active one")
	}
	c, err := s.client()
	if err != nil {
		return "", err
	}
	if emergency {
		// with the active IP down, the panel's own reach to the internet is the
		// control that tells a dead new IP from a panel that is offline
		if err := panelOnline(ctx); err != nil {
			return "", fmt.Errorf("the panel itself cannot reach the internet, so no IP is judged: %w", err)
		}
	} else if err := dialTries(ctx, r.Address, r.TunnelPort, 3*time.Second, 2); err != nil {
		return "", fmt.Errorf("a new IP cannot be judged while the active one is unreachable: %w", err)
	}
	server, err := c.Server(ctx, f.Zone, f.ServerId)
	if err != nil {
		return "", err
	}
	sgIDs := server.SecurityGroupIDs
	if port, err := c.PortByIP(ctx, f.Zone, r.Address); err == nil && len(port.SecurityGroupIDs) > 0 {
		sgIDs = port.SecurityGroupIDs
	}
	day := time.Now().In(s.location()).Format(time.DateOnly)
	adds := addsToday(f, day)

	var added, dead []string
	summary := func() string {
		parts := []string{}
		if len(added) > 0 {
			parts = append(parts, "added "+strings.Join(added, ", "))
		}
		if len(dead) > 0 {
			parts = append(parts, "released dead "+strings.Join(dead, ", "))
		}
		return strings.Join(parts, "; ")
	}
	for len(dead) < arvanMaxDeadTries {
		rows, err := arvanReserves(r.Id)
		if err != nil {
			return summary(), err
		}
		if liveReserves(rows) >= goal {
			return summary(), nil
		}
		if len(added)+len(dead) > 0 {
			if server, err = c.Server(ctx, f.Zone, f.ServerId); err != nil {
				return summary(), err
			}
		}
		if total := max(publicIPv4Count(server), 1+len(rows)); total >= arvanMaxIPs {
			return summary(), fmt.Errorf("the server already holds %d IPs, Arvan's limit", total)
		}
		if adds >= arvanMaxAddsPerDay {
			return summary(), fmt.Errorf("%d IPs were added today already; try again tomorrow", adds)
		}
		ip, verdict, err := s.addOne(ctx, c, r, f, sgIDs, emergency)
		if ip != "" || errors.Is(err, errArvanNoIP) {
			adds++
			_ = updateArvanFront(r.Id, map[string]any{"adds_day": day, "adds_count": adds})
		}
		if err != nil {
			return summary(), err
		}
		if verdict {
			added = append(added, ip)
		} else {
			dead = append(dead, ip)
		}
	}
	return summary(), fmt.Errorf("%d new IPs in a row failed their test; stopped", len(dead))
}

// addOne adds a public IP and tests it: true keeps it as a healthy reserve,
// false means it was dead and is released already.
var errArvanNoIP = errors.New("the new port came back with no IP; it was released")

func (s *ArvanService) addOne(ctx context.Context, c *arvan.Client, r model.Relay, f model.ArvanFront, sgIDs []string, emergency bool) (string, bool, error) {
	port, err := c.AddPublicIP(ctx, f.Zone, f.ServerId, sgIDs)
	if err != nil {
		// a reply lost to a timeout does not mean Arvan made no IP
		stray, ok := waitStrayPort(ctx, c, r, f)
		if !ok {
			return "", false, err
		}
		logger.Warningf("front %s: Arvan's reply to the add was lost (%v); it made %s, which is used", r.Name, err, firstOf(stray.IPs))
		port = stray
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), arvanCallTimeout)
	defer cancel()
	if len(port.IPs) == 0 {
		_ = releasePort(cleanup, c, r.Id, f.Zone, port)
		return "", false, errArvanNoIP
	}
	ip := port.IPs[0]
	row := model.ArvanIP{RelayId: r.Id, IP: ip, PortId: port.ID, Status: ArvanIPTesting}
	if err := database.GetDB().Create(&row).Error; err != nil {
		_ = releasePort(cleanup, c, r.Id, f.Zone, port)
		return ip, false, fmt.Errorf("could not record %s, so it was released: %w", ip, err)
	}
	logger.Infof("front %s: Arvan added %s; testing it", r.Name, ip)
	select {
	case <-ctx.Done():
		return ip, false, ctx.Err()
	case <-time.After(arvanHotplugWait):
	}
	// after the hotplug, not before it; the box's own timer does this too
	if emergency {
		// the active IP cannot be reached to do it, so wait for that timer
		select {
		case <-ctx.Done():
			return ip, false, ctx.Err()
		case <-time.After(arvanTimerWait):
		}
	} else if err := setPrimary(ctx, r, r.Address, r.Address); err != nil {
		logger.Warningf("front %s: could not re-assert the routes after adding %s: %v", r.Name, ip, err)
	}

	ok, iranOk, iranTotal, perr := probeIP(ctx, ip, r.TunnelPort)
	if ctx.Err() != nil {
		return ip, false, ctx.Err()
	}
	if ok {
		saveArvanProbe(row.Id, ok, iranOk, iranTotal, nil)
		return ip, true, nil
	}
	// the panel's own link failing is not a verdict on the new IP
	if emergency {
		if derr := panelOnline(ctx); derr != nil {
			return ip, false, fmt.Errorf("%s left untested: the panel lost the internet: %w", ip, derr)
		}
	} else if derr := dialTries(ctx, r.Address, r.TunnelPort, 3*time.Second, 2); derr != nil {
		return ip, false, fmt.Errorf("%s left untested: the active IP is unreachable too: %w", ip, derr)
	}
	if err := releasePort(cleanup, c, r.Id, f.Zone, port); err != nil {
		saveArvanProbe(row.Id, false, iranOk, iranTotal, fmt.Errorf("%w; release failed: %w", perr, err))
		return ip, false, fmt.Errorf("%s is dead and could not be released: %w", ip, err)
	}
	database.GetDB().Delete(&model.ArvanIP{}, row.Id)
	logger.Warningf("front %s: new IP %s failed its test (%v); released", r.Name, ip, perr)
	return ip, false, nil
}

func saveArvanProbe(rowId int, ok bool, iranOk, iranTotal int, perr error) {
	fields := map[string]any{
		"status": ArvanIPHealthy, "last_check": time.Now().Unix(), "last_error": "",
		"iran_ok": iranOk, "iran_total": iranTotal,
	}
	if !ok {
		fields["status"] = ArvanIPDead
	}
	if perr != nil {
		fields["last_error"] = perr.Error()
	}
	_ = database.GetDB().Model(&model.ArvanIP{}).Where("id = ?", rowId).Updates(fields).Error
}

// recordActive counts the active IP's failed checks toward an auto swap.
func recordActive(relayId int, ok bool) {
	fields := map[string]any{"last_check": time.Now().Unix(), "active_fails": 0}
	if !ok {
		fields["active_fails"] = gorm.Expr("active_fails + 1")
	}
	_ = updateArvanFront(relayId, fields)
}

// releasePort gives an IP back to Arvan for good. The port carrying the
// relay's current address is refused whoever asks.
func releasePort(ctx context.Context, c *arvan.Client, relayId int, zone string, port arvan.Port) error {
	var cur model.Relay
	if err := database.GetDB().First(&cur, relayId).Error; err != nil {
		return err
	}
	// a name hides which port carries the active IP, so nothing is released
	if net.ParseIP(cur.Address) == nil || slices.Contains(port.IPs, cur.Address) {
		return fmt.Errorf("refusing to release the port of the active IP %s", cur.Address)
	}
	f, err := arvanFront(relayId)
	if err != nil {
		return err
	}
	// Arvan's own listing has the last word on what the port carries and whose it is
	ips, owners := slices.Clone(port.IPs), []string{port.InstanceID}
	if live, lerr := c.Ports(ctx, zone); lerr == nil {
		for _, p := range live {
			if p.ID == port.ID {
				ips, owners = append(ips, p.IPs...), append(owners, p.InstanceID)
			}
		}
	}
	for _, owner := range owners {
		if owner != "" && f.ServerId != "" && owner != f.ServerId {
			return fmt.Errorf("refusing to release %s: its port belongs to another server", strings.Join(ips, ", "))
		}
	}
	if len(ips) > 0 {
		var inUse int64
		if err := database.GetDB().Model(&model.Relay{}).Where("address IN ?", ips).Count(&inUse).Error; err != nil {
			return err
		}
		if inUse > 0 {
			return fmt.Errorf("refusing to release %s: a relay uses it as its address", strings.Join(ips, ", "))
		}
	}
	err = c.DeletePort(ctx, zone, port.ID)
	if errors.Is(err, arvan.ErrNotFound) {
		return nil
	}
	return err
}

// releaseIP releases ip by the port Arvan says holds it now; gone already is done.
func releaseIP(ctx context.Context, c *arvan.Client, relayId int, zone, ip string) error {
	port, err := c.PortByIP(ctx, zone, ip)
	if errors.Is(err, arvan.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return releasePort(ctx, c, relayId, zone, port)
}

// Test re-tests the active IP and every reserve.
func (s *ArvanService) Test(relayId int) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	// the same pass the job runs: test, give back the dead and the untracked
	// IPs, and top the reserves up to the target
	return s.start(relayId, "test", func(ctx context.Context) (string, error) {
		return s.sweep(ctx, relayId)
	})
}

func testSummary(active string, activeOK bool, healthy, dead int) string {
	state := "healthy"
	if !activeOK {
		state = "failing"
	}
	return fmt.Sprintf("active %s %s; %d healthy, %d dead reserves", active, state, healthy, dead)
}

// testArvanIPs probes the active IP and each reserve, recording every verdict;
// a probe cut short by the deadline records nothing.
func testArvanIPs(ctx context.Context, r model.Relay) (activeOK bool, healthy, dead int, err error) {
	activeOK, _, _, _ = probeIP(ctx, r.Address, r.TunnelPort)
	if ctx.Err() != nil {
		return false, 0, 0, ctx.Err()
	}
	recordActive(r.Id, activeOK)
	rows, err := arvanReserves(r.Id)
	if err != nil {
		return activeOK, 0, 0, err
	}
	for _, row := range rows {
		if row.IP == r.Address {
			continue
		}
		ok, iranOk, iranTotal, perr := probeIP(ctx, row.IP, r.TunnelPort)
		if ctx.Err() != nil {
			return activeOK, healthy, dead, ctx.Err()
		}
		saveArvanProbe(row.Id, ok, iranOk, iranTotal, perr)
		if ok {
			healthy++
		} else {
			dead++
		}
	}
	return activeOK, healthy, dead, nil
}

// Swap moves the front to target, or to the healthiest reserve when target is "".
func (s *ArvanService) Swap(relayId int, target string) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	target = strings.TrimSpace(target)
	return s.start(relayId, "swap", func(ctx context.Context) (string, error) {
		return s.swap(ctx, relayId, target, false)
	})
}

// healthiestReserve ranks healthy reserves by the share of Iranian nodes that
// reached them, then by the freshest check.
func healthiestReserve(rows []model.ArvanIP, active string) string {
	var healthy []model.ArvanIP
	for _, row := range rows {
		if row.Status == ArvanIPHealthy && row.IP != active {
			healthy = append(healthy, row)
		}
	}
	ratio := func(row model.ArvanIP) float64 {
		if row.IranTotal == 0 {
			return 0
		}
		return float64(row.IranOk) / float64(row.IranTotal)
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		if a, b := ratio(healthy[i]), ratio(healthy[j]); a != b {
			return a > b
		}
		return healthy[i].LastCheck > healthy[j].LastCheck
	})
	if len(healthy) == 0 {
		return ""
	}
	return healthy[0].IP
}

// swap runs in this order: the box's routes via the target, the DNS record,
// the panel's address and tunnel, then the old IP. No reserve: it buys one first.
func (s *ArvanService) swap(ctx context.Context, relayId int, target string, auto bool) (string, error) {
	r, err := arvanRelay(relayId)
	if err != nil {
		return "", err
	}
	f, err := arvanFront(relayId)
	if err != nil {
		return "", err
	}
	if f.ServerId == "" {
		return "", errors.New("detect the Arvan server first")
	}
	if !f.Prepared {
		return "", errors.New("prepare the server first")
	}
	c, err := s.client()
	if err != nil {
		return "", err
	}
	old := r.Address
	if target == old {
		return "", fmt.Errorf("%s is already the active IP", target)
	}
	rows, err := arvanReserves(relayId)
	if err != nil {
		return "", err
	}
	if target == "" {
		target = healthiestReserve(rows, old)
		// with no reserve on hand, "swap now" and the automatic swap both buy one
		if target == "" {
			emergency := dialTries(ctx, old, r.TunnelPort, 3*time.Second, 2) != nil
			if _, err := s.refill(ctx, r, liveReserves(rows)+1, emergency); err != nil {
				return "", fmt.Errorf("no healthy reserve, and adding one failed: %w", err)
			}
			if rows, err = arvanReserves(relayId); err != nil {
				return "", err
			}
			target = healthiestReserve(rows, old)
		}
		if target == "" {
			return "", errors.New("there is no healthy reserve IP to swap to")
		}
	}
	i := slices.IndexFunc(rows, func(row model.ArvanIP) bool { return row.IP == target })
	if i < 0 {
		return "", fmt.Errorf("%s is not a reserve of this front", target)
	}
	row := rows[i]
	ok, iranOk, iranTotal, perr := probeIP(ctx, target, r.TunnelPort)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	saveArvanProbe(row.Id, ok, iranOk, iranTotal, perr)
	if !ok {
		return "", fmt.Errorf("%s failed its test, so the front stays on %s: %w", target, old, perr)
	}

	// Each step undoes the ones before it on failure, so the front is never
	// left half-moved; the old IP still answers through its own table.
	undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), arvanCallTimeout)
	defer cancel()
	revertBox := func() {
		// via the target first: in an auto swap the old IP is the one failing
		err := setPrimary(undo, r, target, old)
		if err != nil {
			err = setPrimary(undo, r, old, old)
		}
		if err != nil {
			logger.Warningf("front %s: could not hand the routes back to %s: %v", r.Name, old, err)
		}
	}
	if err := setPrimary(ctx, r, target, target); err != nil {
		revertBox()
		return "", fmt.Errorf("could not move the front's routes to %s: %w", target, err)
	}
	st := arvanStateOf(relayId)
	var rec arvan.Record
	moveDNS := f.Domain != "" && f.RecordId != ""
	if moveDNS {
		if rec, err = arvanRecordByID(ctx, c, f.Domain, f.RecordId); err == nil {
			err = c.SetARecordIP(ctx, f.Domain, rec, target)
		}
		if err != nil {
			revertBox()
			return "", fmt.Errorf("could not point %s at %s: %w", recordFQDN(f.RecordName, f.Domain), target, err)
		}
		st.setRecordIP(target)
	}
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).Update("address", target).Error; err != nil {
		if moveDNS {
			if derr := c.SetARecordIP(undo, f.Domain, rec, old); derr == nil {
				st.setRecordIP(old)
			}
		}
		revertBox()
		return "", fmt.Errorf("the panel could not record %s as the front's address: %w", target, err)
	}
	database.GetDB().Delete(&model.ArvanIP{}, row.Id)
	r.Address = target
	redialErr := s.redial(r)
	oldNote := s.retireOld(undo, c, r, old)
	now := time.Now().Unix()
	_ = updateArvanFront(relayId, map[string]any{"last_swap": now, "active_fails": 0, "last_check": now})
	logger.Infof("front %s: active IP %s -> %s", r.Name, old, target)
	detail := fmt.Sprintf("%s -> %s; %s", old, target, oldNote)
	if redialErr != nil {
		return detail, fmt.Errorf("the panel could not re-dial the tunnel yet; the relay job retries every minute: %w", redialErr)
	}
	return detail, nil
}

// redial points the panel's tunnel client at the front's new address, as
// reconcileFront does without provisioning the far side again.
func (s *ArvanService) redial(r model.Relay) error {
	// A swap that began before this panel went on standby must not dial beside the new main.
	if global.IsStandby() {
		return nil
	}
	token, err := decryptRelaySecret(r.Id, r.TunnelToken)
	if err != nil {
		return err
	}
	pair, err := s.relays.frontPair(&r, token)
	if err != nil {
		return err
	}
	legs := tunnel.Legs(pair)
	for _, leg := range legs {
		if err := tunnel.GetManager().Ensure(leg); err != nil {
			return err
		}
	}
	tunnel.GetManager().Retain(r.Id, legs)
	return nil
}

// retireOld keeps the old active IP as a reserve when it was healthy and the
// target wants one, and releases it otherwise; a failed release stays listed.
func (s *ArvanService) retireOld(ctx context.Context, c *arvan.Client, r model.Relay, old string) string {
	f, err := arvanFront(r.Id)
	if err != nil {
		return "old IP " + old + " left as it was: " + err.Error()
	}
	rows, _ := arvanReserves(r.Id)
	database.GetDB().Where("ip = ?", old).Delete(&model.ArvanIP{})
	port, perr := c.PortByIP(ctx, f.Zone, old)
	keep := model.ArvanIP{RelayId: r.Id, IP: old, Status: ArvanIPHealthy, LastCheck: f.LastCheck}
	if perr == nil {
		keep.PortId = port.ID
	}
	if f.LastCheck > 0 && f.ActiveFails == 0 && liveReserves(rows) < f.ReserveTarget {
		if err := database.GetDB().Create(&keep).Error; err != nil {
			return "old IP " + old + " kept on the server but not listed: " + err.Error()
		}
		return "kept " + old + " as a reserve"
	}
	if perr == nil {
		perr = releasePort(ctx, c, r.Id, f.Zone, port)
	} else if errors.Is(perr, arvan.ErrNotFound) {
		return "old IP " + old + " is gone already"
	}
	if perr != nil {
		keep.Status, keep.LastError = ArvanIPDead, "release failed: "+perr.Error()
		_ = database.GetDB().Create(&keep).Error
		logger.Warningf("front %s: could not release the old IP %s: %v", r.Name, old, perr)
		return "old IP " + old + " could not be released"
	}
	logger.Infof("front %s: released the old IP %s", r.Name, old)
	return "released " + old
}

// Release gives a reserve back to Arvan; the active IP is refused.
func (s *ArvanService) Release(relayId int, ip string) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return errors.New("which IP should be released?")
	}
	r, err := arvanRelay(relayId)
	if err != nil {
		return err
	}
	if ip == r.Address {
		return fmt.Errorf("%s is the active IP; swap away from it first", ip)
	}
	return s.start(relayId, "release", func(ctx context.Context) (string, error) {
		r, err := arvanRelay(relayId)
		if err != nil {
			return "", err
		}
		if ip == r.Address {
			return "", fmt.Errorf("%s is the active IP; swap away from it first", ip)
		}
		var row model.ArvanIP
		if err := database.GetDB().Where("relay_id = ? AND ip = ?", relayId, ip).First(&row).Error; err != nil {
			return "", fmt.Errorf("%s is not a reserve of this front", ip)
		}
		f, err := arvanFront(relayId)
		if err != nil {
			return "", err
		}
		c, err := s.client()
		if err != nil {
			return "", err
		}
		if err := releaseIP(ctx, c, r.Id, f.Zone, ip); err != nil {
			return "", err
		}
		database.GetDB().Delete(&model.ArvanIP{}, row.Id)
		if err := setPrimary(ctx, r, r.Address, r.Address); err != nil {
			logger.Warningf("front %s: could not re-assert the routes after releasing %s: %v", r.Name, ip, err)
		}
		return "released " + ip, nil
	})
}

// dailyMoment is today's HH:MM in loc.
func dailyMoment(now time.Time, hhmm string, loc *time.Location) (time.Time, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, false
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc), true
}

// Watch is the minute job: check each bound front's active IP, swap when it
// keeps failing or the daily time came, and re-test reserves every 15 minutes.
func (s *ArvanService) Watch() {
	// Checked from a second place, a front could be swapped under the main panel.
	if global.IsStandby() {
		return
	}
	var fronts []model.ArvanFront
	if err := database.GetDB().Where("server_id <> ''").Find(&fronts).Error; err != nil || len(fronts) == 0 {
		return
	}
	if _, readable := s.KeyStatus(); !readable {
		return
	}
	loc := s.location()
	for _, f := range fronts {
		r, err := arvanRelay(f.RelayId)
		if err != nil || !r.Enabled {
			continue
		}
		st := arvanStateOf(f.RelayId)
		if st.busyLabel() != "" || !st.lock.TryLock() {
			continue
		}
		label, run := s.watchOne(st, r, f, loc)
		if run == nil {
			st.lock.Unlock()
			continue
		}
		if _, ok := st.claim(label); !ok {
			st.lock.Unlock()
			continue
		}
		common.GoRecover("arvan-"+label, func() {
			defer st.done()
			defer st.lock.Unlock()
			s.runAction(r.Id, label, run)
		})
	}
}

// watchOne is the cheap part, run under the front's lock; a long action it
// asks for runs behind the job.
func (s *ArvanService) watchOne(st *arvanState, r model.Relay, f model.ArvanFront, loc *time.Location) (string, func(context.Context) (string, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ok := dialTries(ctx, r.Address, r.TunnelPort, 3*time.Second, 2) == nil
	recordActive(r.Id, ok)
	fails := 0
	if !ok {
		fails = f.ActiveFails + 1
	}
	swap := func(ctx context.Context) (string, error) { return s.swap(ctx, r.Id, "", true) }
	now := time.Now()
	if f.AutoSwap && fails >= arvanSwapAfter && st.due(&st.lastAuto, arvanAutoEvery, now) {
		return "swap", swap
	}
	// a slot missed by more than the window waits for tomorrow rather than
	// firing the moment the time is saved
	if at, set := dailyMoment(now, f.DailySwapAt, loc); set && !now.Before(at) && now.Sub(at) < arvanDailyWindow &&
		f.LastSwap < at.Unix() && st.due(&st.lastAuto, arvanAutoEvery, now) {
		return "swap", swap
	}
	var reserves int64
	database.GetDB().Model(&model.ArvanIP{}).Where("relay_id = ?", r.Id).Count(&reserves)
	// the sweep also runs with no reserves: it finds IPs the panel lost track of
	if (reserves > 0 || f.Prepared) && st.due(&st.lastSweep, arvanSweepEvery, now) {
		return "test", func(ctx context.Context) (string, error) { return s.sweep(ctx, r.Id) }
	}
	return "", nil
}

// sweep re-tests everything, releases the reserves found dead and refills to
// the target. Nothing is released unless the active IP passed the same test.
func (s *ArvanService) sweep(ctx context.Context, relayId int) (string, error) {
	r, err := arvanRelay(relayId)
	if err != nil {
		return "", err
	}
	activeOK, healthy, dead, err := testArvanIPs(ctx, r)
	summary := testSummary(r.Address, activeOK, healthy, dead)
	if err != nil {
		return summary, err
	}
	if !activeOK {
		return summary, errors.New("the active IP failed the same test, so no reserve was judged dead")
	}
	f, err := arvanFront(relayId)
	if err != nil {
		return summary, err
	}
	c, err := s.client()
	if err != nil {
		return summary, err
	}
	rows, err := arvanReserves(relayId)
	if err != nil {
		return summary, err
	}
	for _, row := range rows {
		if row.Status != ArvanIPDead || row.IP == r.Address {
			continue
		}
		if err := releaseIP(ctx, c, r.Id, f.Zone, row.IP); err != nil {
			logger.Warningf("front %s: could not release the dead reserve %s: %v", r.Name, row.IP, err)
			continue
		}
		database.GetDB().Delete(&model.ArvanIP{}, row.Id)
		logger.Infof("front %s: released the dead reserve %s", r.Name, row.IP)
		summary += "; released " + row.IP
	}
	if strays, err := s.settleStrays(ctx, c, r, f); err != nil {
		logger.Warningf("front %s: could not look for untracked IPs: %v", r.Name, err)
	} else if strays != "" {
		summary += "; " + strays
	}
	if !f.Prepared || f.ReserveTarget == 0 {
		return summary, nil
	}
	added, err := s.refill(ctx, r, f.ReserveTarget, false)
	if added != "" {
		summary += "; " + added
	}
	return summary, err
}
