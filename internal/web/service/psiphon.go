package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

const (
	psiphonCheckEvery   = 10 * time.Minute
	psiphonRetryEvery   = time.Minute
	psiphonWarmup       = 90 * time.Second
	psiphonTraceTimeout = 20 * time.Second
)

// psiphonTraceURL is a variable so a test can answer it.
var psiphonTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

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
)

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
	if len(st.Regions) == 0 && st.Installed {
		st.Regions = slices.Clone(psiphon.FallbackRegions)
	}

	var rows []model.PsiphonCountry
	if err := database.GetDB().Order("code asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	byCode := make(map[string]model.PsiphonCountry, len(rows))
	codes := slices.Clone(st.Regions)
	for _, r := range rows {
		byCode[r.Code] = r
		if !slices.Contains(codes, r.Code) {
			codes = append(codes, r.Code)
		}
	}
	slices.Sort(codes)
	mgr := psiphon.GetManager()
	for _, code := range codes {
		row := byCode[code]
		live := mgr.Status(code)
		view := PsiphonCountryView{
			Code:      code,
			Name:      psiphonCountryName(code),
			Flag:      relay.FlagFor(code),
			Enabled:   row.Enabled,
			Running:   live.Running,
			Connected: live.Connected,
			Region:    live.Region,
			ExitIP:    row.ExitIP,
			ExitLoc:   row.ExitLoc,
			LastError: row.LastError,
			SocksPort: row.SocksPort,
		}
		// A client that exited says why; that is the error that matters now.
		if row.Enabled && !live.Running && live.Detail != "" {
			view.LastError = live.Detail
		}
		st.Countries = append(st.Countries, view)
	}

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
		known := make(map[string]bool, len(rows))
		for _, r := range rows {
			known[r.Code] = true
			on := slices.Contains(want, r.Code)
			if r.Enabled == on {
				continue
			}
			if !on {
				users, err := psiphonCountryUsers(tx, r.Code)
				if err != nil {
					return err
				}
				if len(users) > 0 {
					return fmt.Errorf("%s is still the exit of %s; take it off there first", psiphonLabel(r.Code), strings.Join(users, ", "))
				}
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

// psiphonCountryUsers names the inbounds leaving through a country: by a host, a
// cisco location or OpenVPN's exit, read from the settings as relay usage reads them.
func psiphonCountryUsers(tx *gorm.DB, code string) ([]string, error) {
	var ids []int
	if err := tx.Model(&model.Host{}).Where("exit_iface = ?", psiphonExit(code)).
		Distinct().Pluck("inbound_id", &ids).Error; err != nil {
		return nil, err
	}
	var sidecars []model.Inbound
	if err := tx.Where("protocol IN ?", []model.Protocol{model.Cisco, model.OpenVPN}).Find(&sidecars).Error; err != nil {
		return nil, err
	}
	for _, ib := range sidecars {
		if slices.Contains(settingsExits(ib.Settings), psiphonExit(code)) && !slices.Contains(ids, ib.Id) {
			ids = append(ids, ib.Id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var inbounds []model.Inbound
	if err := tx.Where("id IN ?", ids).Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(inbounds))
	for i := range inbounds {
		names = append(names, inboundName(&inbounds[i]))
	}
	return names, nil
}

// freePsiphonPorts finds n pairs no other country holds and no local inbound
// claims, so a client never fails to bind behind a country that looks enabled.
func freePsiphonPorts(tx *gorm.DB, rows []model.PsiphonCountry, n int) ([][2]int, error) {
	taken := map[int]bool{}
	for _, r := range rows {
		taken[r.SocksPort], taken[r.HTTPPort] = true, true
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
	var out [][2]int
	for socks := psiphonSocksBase; socks < psiphonHTTPBase && len(out) < n; socks++ {
		httpPort := socks - psiphonSocksBase + psiphonHTTPBase
		if !taken[socks] && !taken[httpPort] {
			out = append(out, [2]int{socks, httpPort})
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("no free pair of ports is left for Psiphon from %d and %d up", psiphonSocksBase, psiphonHTTPBase)
	}
	return out, nil
}

// psiphonPortConflict names the country holding port; a disabled one keeps its
// pair for when it is turned back on, so it counts too.
func psiphonPortConflict(db *gorm.DB, port int) (*portConflictDetail, error) {
	var row model.PsiphonCountry
	err := db.Where("socks_port = ? OR http_port = ?", port, port).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &portConflictDetail{Tag: "psiphon-" + row.Code, Listen: "127.0.0.1", Port: port, Transports: transportTCP}, nil
}

// Wanted is the clients that should run: the enabled countries, once the client
// is on this server; nothing where it cannot run.
func (s *PsiphonService) Wanted() ([]psiphon.Country, error) {
	if psiphon.Supported() != nil || !psiphon.Installed() {
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
	psi := psiphonExits{socks: map[string]int{}, tuns: psiphonTunPossible()}
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
			ip, loc, err := psiphonTrace(ctx, r.SocksPort)
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

// psiphonTrace reads Cloudflare's trace through a country's SOCKS port: the
// address and country the far end sees.
func psiphonTrace(ctx context.Context, socksPort int) (ip, loc string, err error) {
	proxy := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, psiphonTraceURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the trace through Psiphon answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", "", err
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "ip":
			ip = value
		case "loc":
			loc = value
		}
	}
	if ip == "" {
		return "", "", errors.New("the trace through Psiphon named no address")
	}
	return ip, loc, nil
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
