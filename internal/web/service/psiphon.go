package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// A Psiphon country is an exit with no server of ours behind it: one client per
// country runs on this panel, and a host's exit "psi-<CC>" sends its route there.

const psiphonExitPrefix = relay.PsiphonExitPrefix

// The RelayOption kinds, so the forms can list Psiphon countries apart from the
// exit servers.
const (
	RelayOptionRelay   = "relay"
	RelayOptionPsiphon = "psiphon"
)

const (
	psiphonWarpOff   = "off"
	psiphonWarpSites = "sites"
	psiphonWarpAll   = "all"
)

const (
	psiphonRegionsKey = "psiphonRegions"
	psiphonWarpKey    = "psiphonWarp"
)

// A country's SOCKS and HTTP ports pair up from these, below the probe's
// 11179/11379; 200 pairs, far more countries than Psiphon serves.
const (
	psiphonSocksBase = 11181
	psiphonHTTPBase  = 11381
)

// psiphonRedirectOffset puts the port Xray takes a country's redirected cisco and OpenVPN
// pools on 400 above its SOCKS port: 11581 up, past every HTTP port.
const psiphonRedirectOffset = 400

func psiphonRedirectPort(socksPort int) int { return socksPort + psiphonRedirectOffset }

const (
	psiphonCheckEvery   = 10 * time.Minute
	psiphonRetryEvery   = time.Minute
	psiphonWarmup       = 90 * time.Second
	psiphonTraceTimeout = 20 * time.Second
	// PsiphonTestLimit bounds the card's test of the enabled countries, the
	// downloads of the slowest included.
	PsiphonTestLimit = 30 * time.Second
)

var defaultPsiphonWarpSites = []string{
	"domain:gemini.google.com",
	"domain:aistudio.google.com",
	"domain:generativelanguage.googleapis.com",
	"domain:makersuite.google.com",
	"domain:notebooklm.google.com",
	"domain:labs.google",
	"domain:bard.google.com",
	"domain:chatgpt.com",
	"domain:openai.com",
	"domain:claude.ai",
	"domain:anthropic.com",
}

// psiphonCountryNames covers what Psiphon has served; a code missing here is
// shown as itself. A table, since the x/text display tables cost 2.4 MB.
var psiphonCountryNames = map[string]string{
	"AT": "Austria", "AU": "Australia", "BE": "Belgium", "BG": "Bulgaria", "BR": "Brazil",
	"CA": "Canada", "CH": "Switzerland", "CZ": "Czechia", "DE": "Germany", "DK": "Denmark",
	"EE": "Estonia", "ES": "Spain", "FI": "Finland", "FR": "France", "GB": "United Kingdom",
	"HK": "Hong Kong", "HU": "Hungary", "ID": "Indonesia", "IE": "Ireland", "IN": "India",
	"IT": "Italy", "JP": "Japan", "LT": "Lithuania", "LV": "Latvia", "NL": "Netherlands",
	"NO": "Norway", "PL": "Poland", "PT": "Portugal", "RO": "Romania", "RS": "Serbia",
	"SE": "Sweden", "SG": "Singapore", "SK": "Slovakia", "US": "United States",
}

// The two keys stay out of AllSetting, so the settings page never carries them.
func init() {
	defaultValueMap[psiphonRegionsKey] = ""
	defaultValueMap[psiphonWarpKey] = ""
}

// psiphonInstalling keeps a second install or list refresh off the first one's
// probe; psiphonChecking keeps a slow round of exit checks from doubling up.
var (
	psiphonInstalling sync.Mutex
	psiphonChecking   sync.Mutex
	psiphonTesting    sync.Mutex
)

// psiphonTests is each country's last test from the card, kept in memory: it
// is a moment's reading, and the exit it found is saved like the job's check.
var psiphonTests = struct {
	sync.Mutex
	byCode map[string]PsiphonExitTest
}{byCode: map[string]PsiphonExitTest{}}

type PsiphonService struct {
	settings SettingService
}

// PsiphonState is the Psiphon card's whole view: the client, the countries
// Psiphon offers with what each is doing, and WARP behind them.
type PsiphonState struct {
	Supported bool                 `json:"supported"`
	Installed bool                 `json:"installed"`
	Regions   []string             `json:"regions"`
	RegionsAt int64                `json:"regionsAt"`
	Countries []PsiphonCountryView `json:"countries"`
	Warp      PsiphonWarpView      `json:"warp"`
	Scan      PsiphonScanView      `json:"scan"`
}

type PsiphonCountryView struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Flag      string `json:"flag"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Connected bool   `json:"connected"`
	Region    string `json:"region"`
	ExitIP    string `json:"exitIp"`
	ExitLoc   string `json:"exitLoc"`
	LastError string `json:"lastError"`
	SocksPort int    `json:"socksPort"`
	// Saved every 30 s, so up to that much behind what the client counted.
	TrafficDown int64 `json:"trafficDown"`
	TrafficUp   int64 `json:"trafficUp"`
	// Test is the card's last test of the country, nil until one ran.
	Test *PsiphonExitTest `json:"test,omitempty"`
	// UsedBy is what keeps the country from being switched off, never nil.
	UsedBy []PsiphonCountryUse `json:"usedBy"`
}

// The kinds of PsiphonCountryUse.
const (
	psiphonUseHost    = "host"
	psiphonUseCisco   = "cisco"
	psiphonUseOpenVPN = "openvpn"
)

// PsiphonCountryUse is one inbound leaving through a country: Count hosts of it, a
// cisco location of it (named in Location), or OpenVPN's exit.
type PsiphonCountryUse struct {
	Kind     string `json:"kind"`
	Inbound  string `json:"inbound"`
	Count    int    `json:"count"`
	Location string `json:"location,omitempty"`
}

// PsiphonExitTest is one country measured: whether its tunnel was up, where it
// came out, the trace's round trip and the download's speed in bytes a second.
type PsiphonExitTest struct {
	Code      string `json:"code"`
	Connected bool   `json:"connected"`
	ExitIP    string `json:"exitIp"`
	ExitLoc   string `json:"exitLoc"`
	LatencyMs int64  `json:"latencyMs"`
	Speed     int64  `json:"speed"`
	Error     string `json:"error"`
	At        int64  `json:"at"`
}

// PsiphonScanView is the running or last scan of every offered country, each
// tried with a throwaway client; results grow as countries finish.
type PsiphonScanView struct {
	Running    bool              `json:"running"`
	Stopped    bool              `json:"stopped"`
	StartedAt  int64             `json:"startedAt"`
	FinishedAt int64             `json:"finishedAt"`
	Total      int               `json:"total"`
	Results    []PsiphonExitTest `json:"results"`
}

type PsiphonWarpView struct {
	Mode       string `json:"mode"`
	Sites      string `json:"sites"`
	Registered bool   `json:"registered"`
}

// PsiphonInboundChoice is one inbound that can take countries, and the ones its
// hosts already leave through.
type PsiphonInboundChoice struct {
	InboundId int            `json:"inboundId"`
	Remark    string         `json:"remark"`
	Protocol  model.Protocol `json:"protocol"`
	Countries []string       `json:"countries"`
}

type psiphonRegionsSetting struct {
	Regions []string `json:"regions"`
	At      int64    `json:"at"`
}

type psiphonWarpSetting struct {
	Mode  string          `json:"mode"`
	Sites string          `json:"sites"`
	Reg   *psiphonWarpReg `json:"reg,omitempty"`
}

// psiphonWarpReg is integration.WarpService.RegWarpMasque's answer, kept whole.
type psiphonWarpReg struct {
	PrivateKey string   `json:"privateKey"`
	PublicKey  string   `json:"publicKey"`
	Address    []string `json:"address"`
	Endpoint   string   `json:"endpoint"`
}

func psiphonExit(code string) string { return psiphonExitPrefix + code }

// psiphonCode is the country a psi-<CC> exit names, two upper-case letters.
func psiphonCode(exit string) (string, bool) {
	code, ok := strings.CutPrefix(exit, psiphonExitPrefix)
	if !ok || len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return "", false
	}
	return code, true
}

// psiphonRoute is a country's fixed route, its two letters as one number: 16705
// and up, clear of every relay id, and the same for links already handed out.
func psiphonRoute(code string) string { return strconv.Itoa(int(code[0])<<8 | int(code[1])) }

func psiphonCountryName(code string) string {
	if name, ok := psiphonCountryNames[code]; ok {
		return name
	}
	return code
}

func psiphonLabel(code string) string { return relay.LabelFor(psiphonCountryName(code), code) }

// PsiphonExitLabel is the flag and country a psi-<CC> exit leaves from, for the
// subscription's country labels; false for any other exit.
func PsiphonExitLabel(exit string) (string, bool) {
	code, ok := psiphonCode(strings.TrimSpace(exit))
	if !ok {
		return "", false
	}
	return psiphonLabel(code), true
}

// State reads the client, the offered countries and WARP as they are now.
func (s *PsiphonService) State() (*PsiphonState, error) {
	st := &PsiphonState{
		Supported: psiphon.Supported() == nil,
		Installed: psiphon.Installed(),
		Regions:   []string{},
		Countries: []PsiphonCountryView{},
	}
	saved, err := s.savedRegions()
	if err != nil {
		return nil, err
	}
	st.Regions, st.RegionsAt = append(st.Regions, saved.Regions...), saved.At
	if st.Installed {
		st.Regions = psiphonOffered(saved.Regions)
	}

	var rows []model.PsiphonCountry
	if err := database.GetDB().Order("code asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	byCode := make(map[string]model.PsiphonCountry, len(rows))
	for _, r := range rows {
		byCode[r.Code] = r
	}
	codes := psiphonListedCodes(st.Regions, rows)
	uses, err := psiphonCountryUses(database.GetDB())
	if err != nil {
		return nil, err
	}
	mgr := psiphon.GetManager()
	psiphonTests.Lock()
	tests := maps.Clone(psiphonTests.byCode)
	psiphonTests.Unlock()
	for _, code := range codes {
		row := byCode[code]
		live := mgr.Status(code)
		view := PsiphonCountryView{
			Code:        code,
			Name:        psiphonCountryName(code),
			Flag:        relay.FlagFor(code),
			Enabled:     row.Enabled,
			Running:     live.Running,
			Connected:   live.Connected,
			Region:      live.Region,
			ExitIP:      row.ExitIP,
			ExitLoc:     row.ExitLoc,
			LastError:   row.LastError,
			SocksPort:   row.SocksPort,
			TrafficDown: row.TrafficDown,
			TrafficUp:   row.TrafficUp,
			UsedBy:      append([]PsiphonCountryUse{}, uses[code]...),
		}
		// A client that exited says why; that is the error that matters now.
		if row.Enabled && !live.Running && live.Detail != "" {
			view.LastError = live.Detail
		}
		if test, ok := tests[code]; ok {
			view.Test = &test
		}
		st.Countries = append(st.Countries, view)
	}
	st.Scan = psiphonScanView(mgr.ScanState())

	warp, err := s.warpSetting()
	if err != nil {
		return nil, err
	}
	st.Warp = PsiphonWarpView{Mode: warp.Mode, Sites: warp.Sites, Registered: warp.Reg != nil}
	if st.Warp.Mode == "" {
		st.Warp.Mode = psiphonWarpOff
	}
	if strings.TrimSpace(st.Warp.Sites) == "" {
		st.Warp.Sites = strings.Join(defaultPsiphonWarpSites, "\n")
	}
	return st, nil
}

// psiphonListedCodes is every country the card lists: those offered, and those
// with a row that a refresh no longer offers.
func psiphonListedCodes(offered []string, rows []model.PsiphonCountry) []string {
	codes := slices.Clone(offered)
	for _, r := range rows {
		if !slices.Contains(codes, r.Code) {
			codes = append(codes, r.Code)
		}
	}
	slices.Sort(codes)
	return codes
}

// Install fetches the pinned client and reads the countries it serves.
func (s *PsiphonService) Install(ctx context.Context) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	if err := psiphon.Supported(); err != nil {
		return err
	}
	if !psiphonInstalling.TryLock() {
		return errors.New("Psiphon is already being installed or its country list read")
	}
	defer psiphonInstalling.Unlock()
	if _, err := psiphon.Fetch(ctx); err != nil {
		return err
	}
	return s.discoverRegions(ctx)
}

// RefreshRegions reads the countries Psiphon serves again; the old list stays
// if the probe fails.
func (s *PsiphonService) RefreshRegions(ctx context.Context) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	if err := psiphon.Supported(); err != nil {
		return err
	}
	if !psiphon.Installed() {
		return errors.New("install Psiphon first")
	}
	if !psiphonInstalling.TryLock() {
		return errors.New("Psiphon is already being installed or its country list read")
	}
	defer psiphonInstalling.Unlock()
	return s.discoverRegions(ctx)
}

// psiphonOffered is the probe's countries and the known ones. A client lists only the countries
// of the servers it was handed: Poland, Singapore and Canada, missing from one, still connected.
func psiphonOffered(probed []string) []string {
	out := slices.Clone(probed)
	for _, code := range psiphon.FallbackRegions {
		if !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	slices.Sort(out)
	return out
}

func (s *PsiphonService) discoverRegions(ctx context.Context) error {
	regions, err := psiphon.DiscoverRegions(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(psiphonRegionsSetting{Regions: regions, At: time.Now().Unix()})
	if err != nil {
		return err
	}
	return s.settings.setString(psiphonRegionsKey, string(raw))
}

func (s *PsiphonService) savedRegions() (psiphonRegionsSetting, error) {
	var out psiphonRegionsSetting
	raw, err := s.settings.getString(psiphonRegionsKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return psiphonRegionsSetting{}, fmt.Errorf("the saved Psiphon country list is unreadable: %w", err)
	}
	return out, nil
}

func (s *PsiphonService) warpSetting() (psiphonWarpSetting, error) {
	var out psiphonWarpSetting
	raw, err := s.settings.getString(psiphonWarpKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return psiphonWarpSetting{}, fmt.Errorf("the saved Psiphon WARP setting is unreadable: %w", err)
	}
	return out, nil
}

// normalizePsiphonCodes upper-cases, dedupes and sorts codes, refusing any that
// is not two letters.
func normalizePsiphonCodes(codes []string) ([]string, error) {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		code := strings.ToUpper(strings.TrimSpace(c))
		if _, ok := psiphonCode(psiphonExit(code)); !ok {
			return nil, fmt.Errorf("%q is not a two-letter country code", c)
		}
		if !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	slices.Sort(out)
	return out, nil
}

// SetCountries makes codes the enabled countries. One still leaving customers, by a
// host, a cisco location or OpenVPN, is not turned off: they would go nowhere.
func (s *PsiphonService) SetCountries(codes []string) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	want, err := normalizePsiphonCodes(codes)
	if err != nil {
		return err
	}
	saved, err := s.savedRegions()
	if err != nil {
		return err
	}
	for _, code := range want {
		if !slices.Contains(saved.Regions, code) && !slices.Contains(psiphon.FallbackRegions, code) {
			return fmt.Errorf("Psiphon does not offer %s", code)
		}
	}
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		var rows []model.PsiphonCountry
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		uses, err := psiphonCountryUses(tx)
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(rows))
		for _, r := range rows {
			known[r.Code] = true
			on := slices.Contains(want, r.Code)
			if r.Enabled == on {
				continue
			}
			if users := psiphonUseInbounds(uses[r.Code]); !on && len(users) > 0 {
				return fmt.Errorf("%s is still the exit of %s; take it off there first", psiphonLabel(r.Code), strings.Join(users, ", "))
			}
			if err := tx.Model(&model.PsiphonCountry{}).Where("code = ?", r.Code).Update("enabled", on).Error; err != nil {
				return err
			}
		}
		var fresh []string
		for _, code := range want {
			if !known[code] {
				fresh = append(fresh, code)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		ports, err := freePsiphonPorts(tx, rows, len(fresh))
		if err != nil {
			return err
		}
		for i, code := range fresh {
			row := model.PsiphonCountry{Code: code, Enabled: true, SocksPort: ports[i][0], HTTPPort: ports[i][1]}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	(&XrayService{}).SetToNeedRestart()
	wanted, err := s.Wanted()
	if err != nil {
		return err
	}
	if err := psiphon.GetManager().Reconcile(wanted); err != nil {
		return fmt.Errorf("the countries are saved, but not every one could start: %w", err)
	}
	return nil
}

// psiphonCountryUses is, per country, every inbound leaving through it by its hosts, a cisco
// location or OpenVPN's exit, in inbound order. A disabled inbound counts: it can come back.
func psiphonCountryUses(tx *gorm.DB) (map[string][]PsiphonCountryUse, error) {
	var hosts []model.Host
	if err := tx.Where("exit_iface LIKE ?", psiphonExitPrefix+"%").Find(&hosts).Error; err != nil {
		return nil, err
	}
	hostCodes := map[int]map[string]int{}
	ids := []int{}
	for _, h := range hosts {
		code, ok := psiphonCode(h.Exit)
		if !ok {
			continue
		}
		if hostCodes[h.InboundId] == nil {
			hostCodes[h.InboundId] = map[string]int{}
			ids = append(ids, h.InboundId)
		}
		hostCodes[h.InboundId][code]++
	}
	sidecars := []model.Protocol{model.Cisco, model.OpenVPN}
	q := tx.Where("protocol IN ?", sidecars)
	if len(ids) > 0 {
		q = tx.Where("id IN ? OR protocol IN ?", ids, sidecars)
	}
	var inbounds []model.Inbound
	if err := q.Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	out := map[string][]PsiphonCountryUse{}
	for i := range inbounds {
		ib := &inbounds[i]
		name := inboundName(ib)
		codes := slices.Sorted(maps.Keys(hostCodes[ib.Id]))
		for _, code := range codes {
			out[code] = append(out[code], PsiphonCountryUse{Kind: psiphonUseHost, Inbound: name, Count: hostCodes[ib.Id][code]})
		}
		if ib.Protocol != model.Cisco && ib.Protocol != model.OpenVPN {
			continue
		}
		kind := psiphonUseCisco
		if ib.Protocol == model.OpenVPN {
			kind = psiphonUseOpenVPN
		}
		for _, place := range settingsExitPlaces(ib.Settings) {
			if code, ok := psiphonCode(place.exit); ok {
				out[code] = append(out[code], PsiphonCountryUse{Kind: kind, Inbound: name, Count: 1, Location: place.location})
			}
		}
	}
	return out, nil
}

// psiphonUseInbounds is the inbounds of uses, each named once, in their order.
func psiphonUseInbounds(uses []PsiphonCountryUse) []string {
	var names []string
	for _, u := range uses {
		if !slices.Contains(names, u.Inbound) {
			names = append(names, u.Inbound)
		}
	}
	return names
}

// psiphonTakenPorts is every port a country holds, its redirect port too, and
// every port a local inbound claims.
func psiphonTakenPorts(tx *gorm.DB, rows []model.PsiphonCountry) (map[int]bool, error) {
	taken := map[int]bool{}
	for _, r := range rows {
		taken[r.SocksPort], taken[r.HTTPPort], taken[psiphonRedirectPort(r.SocksPort)] = true, true, true
	}
	var inbounds []*model.Inbound
	if err := tx.Where("node_id IS NULL").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	for _, ib := range inbounds {
		for _, c := range portClaims(ib) {
			taken[c.port] = true
		}
	}
	return taken, nil
}

// freePsiphonPorts finds n pairs no other country holds and no local inbound
// claims, so a client never fails to bind behind a country that looks enabled.
func freePsiphonPorts(tx *gorm.DB, rows []model.PsiphonCountry, n int) ([][2]int, error) {
	taken, err := psiphonTakenPorts(tx, rows)
	if err != nil {
		return nil, err
	}
	var out [][2]int
	for socks := psiphonSocksBase; socks < psiphonHTTPBase && len(out) < n; socks++ {
		httpPort := socks - psiphonSocksBase + psiphonHTTPBase
		if !taken[socks] && !taken[httpPort] && !taken[psiphonRedirectPort(socks)] {
			out = append(out, [2]int{socks, httpPort})
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("no free pair of ports is left for Psiphon from %d and %d up", psiphonSocksBase, psiphonHTTPBase)
	}
	return out, nil
}

// psiphonPortConflict names the country holding a TCP port: its two loopback ports when
// loopback, and on every address its redirect port. A disabled one keeps them all.
func psiphonPortConflict(db *gorm.DB, port int, loopback bool, ignoreId int) (*portConflictDetail, error) {
	var row model.PsiphonCountry
	err := db.Where("socks_port = ?", port-psiphonRedirectOffset).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		kept, err := tcpPortStored(db, ignoreId, port)
		if err != nil || kept {
			return nil, err
		}
		return &portConflictDetail{Tag: psiphonRedirectTag(psiphonExit(row.Code)), Listen: "0.0.0.0", Port: port, Transports: transportTCP}, nil
	}
	if !loopback {
		return nil, nil
	}
	if psiphon.GetManager().ScanHolds(port) {
		return &portConflictDetail{Tag: "psiphon-scan", Listen: "127.0.0.1", Port: port, Transports: transportTCP}, nil
	}
	err = db.Where("socks_port = ? OR http_port = ?", port, port).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &portConflictDetail{Tag: "psiphon-" + row.Code, Listen: "127.0.0.1", Port: port, Transports: transportTCP}, nil
}

// tcpPortStored reports whether stored row id already takes TCP on port. One saved there
// before redirect ports existed keeps it; its country's pools are held instead.
func tcpPortStored(db *gorm.DB, id, port int) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	var stored model.Inbound
	err := db.Where("id = ?", id).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stored.NodeID == nil && claimedOn(portClaims(&stored), port)&transportTCP != 0, nil
}

// psiphonRunsHere is a variable so a test can want countries on any system.
var psiphonRunsHere = psiphon.Supported

// Wanted is the clients that should run, the client fetched first where it is missing (a restore,
// a promoted copy, a newly pinned build); nothing where it cannot run.
func (s *PsiphonService) Wanted() ([]psiphon.Country, error) {
	if psiphonRunsHere() != nil {
		return nil, nil
	}
	var rows []model.PsiphonCountry
	if err := database.GetDB().Where("enabled = ?", true).Order("code asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]psiphon.Country, 0, len(rows))
	for _, r := range rows {
		out = append(out, psiphon.Country{Code: r.Code, SocksPort: r.SocksPort, HTTPPort: r.HTTPPort})
	}
	return out, nil
}

// InboundChoices lists the local inbounds a country can be given to.
func (s *PsiphonService) InboundChoices() ([]PsiphonInboundChoice, error) {
	db := database.GetDB()
	var inbounds []model.Inbound
	if err := db.Where("node_id IS NULL AND protocol IN ?", []model.Protocol{model.VLESS, model.Hysteria, model.WireGuard}).
		Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	var hosts []model.Host
	if err := db.Where("exit_iface LIKE ?", psiphonExitPrefix+"%").Find(&hosts).Error; err != nil {
		return nil, err
	}
	countries := map[int][]string{}
	for _, h := range hosts {
		if code, ok := psiphonCode(h.Exit); ok && !slices.Contains(countries[h.InboundId], code) {
			countries[h.InboundId] = append(countries[h.InboundId], code)
		}
	}
	out := make([]PsiphonInboundChoice, 0, len(inbounds))
	for i := range inbounds {
		ib := &inbounds[i]
		codes := append([]string{}, countries[ib.Id]...)
		slices.Sort(codes)
		out = append(out, PsiphonInboundChoice{InboundId: ib.Id, Remark: inboundName(ib), Protocol: ib.Protocol, Countries: codes})
	}
	return out, nil
}

// SetInboundCountries gives an inbound one host per country, cloned from its own
// first host, so each country is one more config in the inbound's subscription.
func (s *PsiphonService) SetInboundCountries(inboundId int, codes []string) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	want, err := normalizePsiphonCodes(codes)
	if err != nil {
		return err
	}
	changed := false
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		var ib model.Inbound
		if err := tx.First(&ib, inboundId).Error; err != nil {
			return common.NewError("inbound not found")
		}
		if ib.Protocol == model.WireGuard && len(want) > 1 {
			return fmt.Errorf("%s is WireGuard, whose link carries no route: it can leave through one country only", inboundName(&ib))
		}

		var hosts []*model.Host
		if err := tx.Where("inbound_id = ?", inboundId).Order("sort_order asc, id asc").Find(&hosts).Error; err != nil {
			return err
		}
		var have, drop []string
		for _, h := range hosts {
			code, ok := psiphonCode(h.Exit)
			if !ok {
				continue
			}
			have = append(have, code)
			if !slices.Contains(want, code) && !slices.Contains(drop, h.Exit) {
				drop = append(drop, h.Exit)
			}
		}
		if len(drop) > 0 {
			if _, err := database.DeleteHosts(tx, "inbound_id = ? AND exit_iface IN ?", inboundId, drop); err != nil {
				return err
			}
			changed = true
		}
		var add []string
		for _, code := range want {
			if !slices.Contains(have, code) {
				add = append(add, code)
			}
		}
		if len(add) == 0 {
			return nil
		}
		changed = true

		var top int
		if err := tx.Model(&model.Host{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&top).Error; err != nil {
			return err
		}
		sortOrder := top + 1
		// The subscription lists only hosts once an inbound has any, so its own
		// config gets a host first; WireGuard's one exit takes the whole inbound.
		if len(hosts) == 0 && ib.Protocol != model.WireGuard {
			own := &entity.HostGroup{InboundIds: []int{inboundId}, Remark: inboundName(&ib), Security: "same", RandomCount: 1, SortOrder: sortOrder}
			if err := addHostGroupIn(tx, own); err != nil {
				return err
			}
			sortOrder++
		}
		source := psiphonHostSource(hosts)
		for _, code := range add {
			req := *source
			req.GroupId, req.InboundIds, req.SortOrder = "", []int{inboundId}, sortOrder
			req.Remark, req.Exit, req.VlessRoute = psiphonLabel(code), psiphonExit(code), ""
			req.IsDisabled, req.IsHidden = false, false
			if err := addHostGroupIn(tx, &req); err != nil {
				return fmt.Errorf("%s: %w", psiphonLabel(code), err)
			}
			sortOrder++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		(&XrayService{}).SetToNeedRestart()
	}
	return nil
}

// psiphonHostSource is the group a new country's host copies its connection
// from: the inbound's first host that leaves elsewhere, an enabled one first.
func psiphonHostSource(hosts []*model.Host) *entity.HostGroup {
	var first *model.Host
	for _, h := range hosts {
		if strings.HasPrefix(h.Exit, psiphonExitPrefix) {
			continue
		}
		if first == nil || (first.IsDisabled && !h.IsDisabled) {
			first = h
		}
	}
	if first == nil {
		return &entity.HostGroup{Security: "same", RandomCount: 1}
	}
	var group []*model.Host
	for _, h := range hosts {
		if h.Id == first.Id || (first.GroupId != "" && h.GroupId == first.GroupId) {
			group = append(group, h)
		}
	}
	source := groupHosts(group)[0]
	// Each address already carries its own port; the group's would land on the
	// ones that had none.
	source.Port = 0
	return source
}

// addHostGroupIn writes one host group the way the Hosts page's add does, in the
// caller's transaction, so a refused country leaves the inbound as it was.
func addHostGroupIn(tx *gorm.DB, req *entity.HostGroup) error {
	if err := validateFinalMaskJSONSalamander(req.FinalMask); err != nil {
		return err
	}
	if err := validateInboundsExist(tx, req.InboundIds); err != nil {
		return err
	}
	if err := settleHostExit(tx, "", req); err != nil {
		return err
	}
	rows := buildHostRows(random.NumLower(16), req)
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}

// SetWarp sets how WARP rides behind the countries. register enrolls a WARP
// device the first time one is needed; integration's WarpService imports this package.
func (s *PsiphonService) SetWarp(mode, sites string, register func() (string, error)) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	mode = strings.TrimSpace(mode)
	if mode != psiphonWarpOff && mode != psiphonWarpSites && mode != psiphonWarpAll {
		return fmt.Errorf("WARP mode %q is not off, sites or all", mode)
	}
	list, err := normalizeWarpSites(sites)
	if err != nil {
		return err
	}
	if mode == psiphonWarpSites && len(list) == 0 {
		return errors.New("list at least one site for WARP, or send all traffic through it")
	}
	cur, err := s.warpSetting()
	if err != nil {
		return err
	}
	cur.Mode, cur.Sites = mode, strings.Join(list, "\n")
	if mode != psiphonWarpOff && cur.Reg == nil {
		raw, err := register()
		if err != nil {
			return fmt.Errorf("could not register a WARP device: %w", err)
		}
		var reg psiphonWarpReg
		if err := json.Unmarshal([]byte(raw), &reg); err != nil {
			return fmt.Errorf("WARP answered a registration that is unreadable: %w", err)
		}
		if reg.Endpoint == "" || reg.PrivateKey == "" || reg.PublicKey == "" || len(reg.Address) == 0 {
			return errors.New("WARP answered a registration without its endpoint, keys or address")
		}
		cur.Reg = &reg
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	if err := s.settings.setString(psiphonWarpKey, string(raw)); err != nil {
		return err
	}
	(&XrayService{}).SetToNeedRestart()
	return nil
}

var warpSiteDomain = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)

// normalizeWarpSites reads one Xray domain rule per line, a bare domain meaning
// domain:, and refuses what would stop Xray from starting.
func normalizeWarpSites(text string) ([]string, error) {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rule, err := warpSiteRule(line)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, rule) {
			out = append(out, rule)
		}
	}
	return out, nil
}

func warpSiteRule(line string) (string, error) {
	kind, value, found := strings.Cut(line, ":")
	if !found {
		kind, value = "domain", line
	}
	kind = strings.ToLower(kind)
	switch kind {
	case "domain", "full":
		value = strings.TrimSuffix(strings.ToLower(value), ".")
		if !warpSiteDomain.MatchString(value) {
			return "", fmt.Errorf("WARP site %q is not a domain", line)
		}
	case "keyword":
		if value == "" || strings.ContainsAny(value, " \t") {
			return "", fmt.Errorf("WARP site %q has no keyword, or one with spaces", line)
		}
	case "regexp":
		if _, err := regexp.Compile(value); value == "" || err != nil {
			return "", fmt.Errorf("WARP site %q is not a regular expression Xray can read", line)
		}
	case "geosite":
		if issues := (&GeodataService{}).Validate(false, []string{kind + ":" + value}); len(issues) > 0 {
			return "", fmt.Errorf("WARP site %q: %s in the geosite database", line, issues[0].Reason)
		}
	default:
		return "", fmt.Errorf("WARP site %q: write a domain, or a domain:, full:, keyword:, regexp: or geosite: rule", line)
	}
	return kind + ":" + value, nil
}

// loadPsiphonExits reads what the Xray config needs of the countries and WARP.
func loadPsiphonExits() psiphonExits {
	psi := psiphonExits{socks: map[string]int{}}
	var settings SettingService
	if port, err := settings.GetPort(); err == nil && port > 0 {
		psi.panelPorts = append(psi.panelPorts, port)
	}
	if on, _ := settings.GetSubEnable(); on {
		if port, err := settings.GetSubPort(); err == nil && port > 0 {
			psi.panelPorts = append(psi.panelPorts, port)
		}
	}
	var rows []model.PsiphonCountry
	if err := database.GetDB().Where("enabled = ?", true).Find(&rows).Error; err != nil {
		logger.Warning("exit routes: could not read the Psiphon countries:", err)
		return psi
	}
	for _, r := range rows {
		psi.socks[r.Code] = r.SocksPort
	}
	warp, err := (&PsiphonService{}).warpSetting()
	if err != nil {
		logger.Warning("exit routes:", err)
		return psi
	}
	if warp.Reg == nil || warp.Mode == "" || warp.Mode == psiphonWarpOff {
		return psi
	}
	psi.warp, psi.warpMode = warp.Reg, warp.Mode
	for line := range strings.SplitSeq(warp.Sites, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			psi.warpSites = append(psi.warpSites, line)
		}
	}
	return psi
}

// exitOptions is the enabled countries as the exit dropdown offers them.
func (s *PsiphonService) exitOptions() ([]RelayOption, error) {
	var rows []model.PsiphonCountry
	if err := database.GetDB().Where("enabled = ?", true).Order("code asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	mgr := psiphon.GetManager()
	out := make([]RelayOption, 0, len(rows))
	for _, r := range rows {
		name := psiphonCountryName(r.Code)
		out = append(out, RelayOption{
			Interface:   psiphonExit(r.Code),
			Name:        "Psiphon " + r.Code,
			Label:       psiphonLabel(r.Code),
			Country:     name,
			CountryCode: r.Code,
			Ready:       mgr.Status(r.Code).Connected,
			Kind:        RelayOptionPsiphon,
		})
	}
	return out, nil
}

// CheckExits asks each enabled country, through its own client, where it leaves
// from: ten minutes apart once known, a minute apart while it fails.
func (s *PsiphonService) CheckExits() {
	if !psiphonChecking.TryLock() {
		return
	}
	defer psiphonChecking.Unlock()
	var rows []model.PsiphonCountry
	if err := database.GetDB().Where("enabled = ?", true).Find(&rows).Error; err != nil {
		logger.Warning("psiphon: could not read the countries:", err)
		return
	}
	mgr := psiphon.GetManager()
	now := time.Now()
	var wg sync.WaitGroup
	for _, r := range rows {
		live := mgr.Status(r.Code)
		if !live.Running || !psiphonCheckDue(r, live, now) {
			continue
		}
		// A client still connecting is silent, not broken.
		young := now.Sub(live.Since) < psiphonWarmup
		if young && !live.Connected {
			continue
		}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), psiphonTraceTimeout)
			defer cancel()
			ip, loc, _, err := psiphon.Trace(ctx, r.SocksPort)
			if err != nil && young {
				return
			}
			recordPsiphonCheck(r.Code, ip, loc, err, time.Now())
		})
	}
	wg.Wait()
}

func psiphonCheckDue(r model.PsiphonCountry, live psiphon.Status, now time.Time) bool {
	if r.LastCheck == 0 {
		return true
	}
	last := time.Unix(r.LastCheck, 0)
	// A new client is a new tunnel, which may leave elsewhere.
	if live.Since.After(last) {
		return true
	}
	if r.ExitIP == "" || r.LastError != "" {
		return now.Sub(last) >= psiphonRetryEvery
	}
	return now.Sub(last) >= psiphonCheckEvery
}

func recordPsiphonCheck(code, ip, loc string, err error, at time.Time) {
	fields := map[string]any{"last_check": at.Unix(), "last_error": ""}
	if err != nil {
		fields["last_error"] = err.Error()
	} else {
		fields["exit_ip"], fields["exit_loc"] = ip, loc
	}
	if dbErr := database.GetDB().Model(&model.PsiphonCountry{}).Where("code = ?", code).Updates(fields).Error; dbErr != nil {
		logger.Warning("psiphon: could not record ", code, "'s exit:", dbErr)
	}
}

// TestEnabled measures every enabled country now, through the client already running
// for it; the exit each found is saved as the job's check saves it.
func (s *PsiphonService) TestEnabled(ctx context.Context) ([]PsiphonExitTest, error) {
	if global.IsStandby() {
		return nil, global.ErrStandby
	}
	if !psiphonTesting.TryLock() {
		return nil, errors.New("the Psiphon countries are already being tested")
	}
	defer psiphonTesting.Unlock()
	wanted, err := s.Wanted()
	if err != nil {
		return nil, err
	}
	return recordPsiphonTests(psiphon.GetManager().TestCountries(ctx, wanted), time.Now()), nil
}

// recordPsiphonTests keeps each test for the card and saves its exit like the job's
// check: a trace that answered is the exit even when the download then failed.
func recordPsiphonTests(tested []psiphon.CountryTest, now time.Time) []PsiphonExitTest {
	out := make([]PsiphonExitTest, 0, len(tested))
	for _, t := range tested {
		var traceErr error
		if t.Exit.IP == "" {
			traceErr = t.Exit.Err
		}
		if !errors.Is(t.Exit.Err, psiphon.ErrNotReached) {
			recordPsiphonCheck(t.Code, t.Exit.IP, t.Exit.Loc, traceErr, now)
		}
		out = append(out, psiphonExitTest(t.Code, t.Connected, t.Exit, "", now))
	}
	psiphonTests.Lock()
	for _, view := range out {
		psiphonTests.byCode[view.Code] = view
	}
	psiphonTests.Unlock()
	return out
}

func psiphonExitTest(code string, connected bool, exit psiphon.Exit, detail string, at time.Time) PsiphonExitTest {
	view := PsiphonExitTest{
		Code: code, Connected: connected, ExitIP: exit.IP, ExitLoc: exit.Loc,
		LatencyMs: exit.Latency.Milliseconds(), Speed: exit.BytesPerSec, Error: detail, At: at.Unix(),
	}
	if exit.Err != nil {
		view.Error = exit.Err.Error()
	}
	return view
}

func psiphonScanView(st psiphon.ScanState) PsiphonScanView {
	view := PsiphonScanView{Running: st.Running, Stopped: st.Stopped, Total: st.Total, Results: []PsiphonExitTest{}}
	if !st.Started.IsZero() {
		view.StartedAt = st.Started.Unix()
	}
	if !st.Finished.IsZero() {
		view.FinishedAt = st.Finished.Unix()
	}
	for _, r := range st.Results {
		view.Results = append(view.Results, psiphonExitTest(r.Code, r.Connected, r.Exit, r.Detail, st.Started))
	}
	return view
}

// StartScan tries every country the card lists, enabled or not, each with a throwaway
// client on ports of its own; it runs in the background and answers at once.
func (s *PsiphonService) StartScan() error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	if err := psiphonRunsHere(); err != nil {
		return err
	}
	if !psiphon.Installed() {
		return errors.New("install Psiphon first")
	}
	saved, err := s.savedRegions()
	if err != nil {
		return err
	}
	var rows []model.PsiphonCountry
	if err := database.GetDB().Find(&rows).Error; err != nil {
		return err
	}
	taken, err := psiphonTakenPorts(database.GetDB(), rows)
	if err != nil {
		return err
	}
	mgr := psiphon.GetManager()
	codes := psiphonListedCodes(psiphonOffered(saved.Regions), rows)
	if err := mgr.StartScan(codes, func(port int) bool { return taken[port] }); err != nil {
		return err
	}
	// Standby set after the check above stood down before the scan existed, so the
	// scan is stopped here instead.
	if global.IsStandby() {
		mgr.StopScan()
		return global.ErrStandby
	}
	return nil
}

// FlushTraffic adds what each country carried since the last flush to its row,
// a country switched off since then included.
func (s *PsiphonService) FlushTraffic() error {
	return psiphon.GetManager().FlushTraffic(addPsiphonTraffic)
}

func addPsiphonTraffic(moved map[string]psiphon.Traffic) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		for code, by := range moved {
			err := tx.Model(&model.PsiphonCountry{}).Where("code = ?", code).Updates(map[string]any{
				"traffic_down": gorm.Expr("traffic_down + ?", by.Down),
				"traffic_up":   gorm.Expr("traffic_up + ?", by.Up),
			}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ResetTraffic sets one country's counts back to zero, or every country's with all; a request
// naming neither is refused, so a misspelt field never wipes every count.
func (s *PsiphonService) ResetTraffic(code string, all bool) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	named := strings.TrimSpace(code) != ""
	if named == all {
		return errors.New("name one Psiphon country to reset, or all of them")
	}
	var codes []string
	if named {
		var err error
		if codes, err = normalizePsiphonCodes([]string{code}); err != nil {
			return err
		}
	}
	return psiphon.GetManager().ResetTraffic(codes, func() error {
		q := database.GetDB().Model(&model.PsiphonCountry{}).Where("1 = 1")
		if len(codes) > 0 {
			q = q.Where("code IN ?", codes)
		}
		res := q.Updates(map[string]any{"traffic_down": 0, "traffic_up": 0})
		if res.Error == nil && len(codes) > 0 && res.RowsAffected == 0 {
			return fmt.Errorf("there is no Psiphon country %s", codes[0])
		}
		return res.Error
	})
}
