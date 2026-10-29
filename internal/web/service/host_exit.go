package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// A host with an exit is a country a customer picks: its route rides the third
// group of a VLESS UUID or a Hysteria auth, which Xray masks out to find the user.

// ExitOutboundTag is the Xray outbound that leaves through one exit server.
func ExitOutboundTag(iface string) string { return "exit-" + iface }

// exitKind is how an inbound's sessions find their exit: by the route in each
// link, or all of them by the inbound's tag, a WireGuard link carrying no route.
type exitKind int

const (
	exitNone exitKind = iota
	exitByRoute
	exitWholeInbound
)

func exitKindOf(p model.Protocol) exitKind {
	switch p {
	case model.VLESS, model.Hysteria:
		return exitByRoute
	case model.WireGuard:
		return exitWholeInbound
	}
	return exitNone
}

// HysteriaAuthCarriesRoute: a UUID, dashed or as 32 hex characters, the two
// spellings a route can be written into and the auth still kept as it is spelt.
func HysteriaAuthCarriesRoute(auth string) bool {
	if len(auth) != 32 && len(auth) != 36 {
		return false
	}
	_, err := uuid.Parse(auth)
	return err == nil
}

// settleHostExit checks a host's exit and routes it by the exit's id unless one was
// typed. An update's own rows are replaced, so they never clash; an add passes "".
func settleHostExit(tx *gorm.DB, groupId string, req *entity.HostGroup) error {
	req.Exit = strings.TrimSpace(req.Exit)
	if req.Exit == "" {
		var wireguard []model.Inbound
		if err := tx.Where("id IN ? AND protocol = ?", req.InboundIds, model.WireGuard).Find(&wireguard).Error; err != nil {
			return err
		}
		return oneCountryPerWireGuardInbound(tx, groupId, wireguard, "")
	}
	defaultRoute, err := exitDefaultRoute(tx, req.Exit)
	if err != nil {
		return err
	}

	var inbounds []model.Inbound
	if err := tx.Where("id IN ?", req.InboundIds).Find(&inbounds).Error; err != nil {
		return err
	}
	var routed []int
	var whole, hysteria []model.Inbound
	for _, ib := range inbounds {
		name := inboundName(&ib)
		switch exitKindOf(ib.Protocol) {
		case exitByRoute:
			routed = append(routed, ib.Id)
		case exitWholeInbound:
			whole = append(whole, ib)
		default:
			return fmt.Errorf("an exit country needs a VLESS, Hysteria or WireGuard inbound (AmneziaWG and TUIC cannot take one), and %s is %s", name, ib.Protocol)
		}
		if ib.NodeID != nil {
			return fmt.Errorf("%s runs on a node, whose traffic never reaches this server's exits", name)
		}
		if ib.Protocol == model.Hysteria {
			hysteria = append(hysteria, ib)
		}
	}

	req.VlessRoute = strings.TrimSpace(req.VlessRoute)
	if req.VlessRoute == "" && len(routed) > 0 {
		req.VlessRoute = defaultRoute
	}
	if req.VlessRoute != "" {
		if n, err := strconv.Atoi(req.VlessRoute); err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("VLESS route %q is not a number from 0 to 65535", req.VlessRoute)
		}
	}

	// One route picks one exit on an inbound; a second would be decided by rule
	// order, silently.
	if len(routed) > 0 {
		var clash model.Host
		err := tx.Where("inbound_id IN ? AND vless_route = ? AND exit_iface <> '' AND exit_iface <> ? AND group_id <> ?",
			routed, req.VlessRoute, req.Exit, groupId).First(&clash).Error
		if err == nil {
			return fmt.Errorf("VLESS route %s already leaves through %s in host %q; give this one another route", req.VlessRoute, clash.Exit, clash.Remark)
		}
	}
	if err := oneCountryPerWireGuardInbound(tx, groupId, whole, req.Exit); err != nil {
		return err
	}
	for _, ib := range hysteria {
		if err := checkHysteriaAuthsCarryRoute(tx, ib.Id, inboundName(&ib)); err != nil {
			return err
		}
	}
	return nil
}

// exitDefaultRoute checks that an exit exists and returns the route its hosts take
// unless one is typed: a relay's id, or a Psiphon country's letters (psiphonRoute).
func exitDefaultRoute(tx *gorm.DB, exit string) (string, error) {
	if strings.HasPrefix(exit, psiphonExitPrefix) {
		code, ok := psiphonCode(exit)
		if !ok {
			return "", fmt.Errorf("%q is not a Psiphon country", exit)
		}
		var country model.PsiphonCountry
		if err := tx.Where("code = ? AND enabled = ?", code, true).First(&country).Error; err != nil {
			return "", fmt.Errorf("there is no enabled Psiphon country %s", code)
		}
		return psiphonRoute(code), nil
	}
	id, ok := relay.InterfaceID(exit)
	if !ok {
		return "", fmt.Errorf("%q is not an exit server", exit)
	}
	var server model.Relay
	if err := tx.Where("id = ? AND role = ?", id, model.RelayExit).First(&server).Error; err != nil {
		return "", fmt.Errorf("there is no exit server %s", exit)
	}
	if _, err := relay.XrayExitSource(id); err != nil {
		return "", err
	}
	return strconv.Itoa(id), nil
}

// checkSidecarPsiphonExits refuses a cisco or OpenVPN psi- exit that is no enabled
// Psiphon country, or one on a node, whose pools never reach this server's Psiphon.
func checkSidecarPsiphonExits(db *gorm.DB, ib *model.Inbound) error {
	if ib.Protocol != model.Cisco && ib.Protocol != model.OpenVPN {
		return nil
	}
	for _, exit := range settingsExits(ib.Settings) {
		if !strings.HasPrefix(exit, psiphonExitPrefix) {
			continue
		}
		if ib.NodeID != nil {
			return fmt.Errorf("%s runs on a node, whose pools never reach this server's Psiphon", inboundName(ib))
		}
		if _, err := exitDefaultRoute(db, exit); err != nil {
			return err
		}
		if err := psiphonRedirectFree(db, ib, exit); err != nil {
			return err
		}
	}
	return nil
}

// psiphonRedirectFree refuses exit while an inbound here, ib or a stored one, takes TCP on
// the port Xray would take ib's pools on: the core could not bind it, and they stay held.
func psiphonRedirectFree(db *gorm.DB, ib *model.Inbound, exit string) error {
	code, _ := psiphonCode(exit)
	var country model.PsiphonCountry
	if err := db.Where("code = ?", code).First(&country).Error; err != nil {
		return err
	}
	port := psiphonRedirectPort(country.SocksPort)
	var others []*model.Inbound
	q := db.Where("port = ? AND node_id IS NULL", port)
	if ib.Id > 0 {
		q = q.Where("id != ?", ib.Id)
	}
	if err := q.Find(&others).Error; err != nil {
		return err
	}
	for _, other := range append(others, ib) {
		if claimedOn(portClaims(other), port)&transportTCP != 0 {
			return fmt.Errorf("%s leaves through Psiphon %s, whose cisco and OpenVPN pools Xray takes on TCP port %d, which %s already uses: give that inbound another port",
				inboundName(ib), code, port, inboundName(other))
		}
	}
	return nil
}

func inboundName(ib *model.Inbound) string {
	if ib.Remark != "" {
		return ib.Remark
	}
	return ib.Tag
}

// oneCountryPerWireGuardInbound: the inbound's one exit rule takes every session, so
// a host leaving elsewhere, this server ("") too, would name a country it never reaches.
func oneCountryPerWireGuardInbound(tx *gorm.DB, groupId string, inbounds []model.Inbound, exit string) error {
	if len(inbounds) == 0 {
		return nil
	}
	ids := make([]int, len(inbounds))
	names := make(map[int]string, len(inbounds))
	for i := range inbounds {
		ids[i] = inbounds[i].Id
		names[ids[i]] = inboundName(&inbounds[i])
	}
	var clash model.Host
	err := tx.Where("inbound_id IN ? AND COALESCE(exit_iface, '') <> ? AND group_id <> ?", ids, exit, groupId).First(&clash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	country := clash.Exit
	if country == "" {
		country = "this server"
	}
	return fmt.Errorf("%s already leaves through %s in host %q: a WireGuard link carries no route, so one country takes the whole inbound; give each country its own inbound",
		names[clash.InboundId], country, clash.Remark)
}

// hysteriaAuthsWithoutRoute names the first few clients whose auth no route
// fits in, or "" when every one can carry it.
func hysteriaAuthsWithoutRoute(clients []model.Client) string {
	const shown = 5
	var bad []string
	for _, c := range clients {
		if !HysteriaAuthCarriesRoute(c.Auth) {
			bad = append(bad, c.Email)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	list := strings.Join(bad[:min(len(bad), shown)], ", ")
	if len(bad) > shown {
		list += fmt.Sprintf(" and %d more", len(bad)-shown)
	}
	return list
}

// checkHysteriaAuthsCarryRoute refuses an exit on a Hysteria inbound while a
// client's auth cannot carry the route, naming the first few to regenerate.
func checkHysteriaAuthsCarryRoute(tx *gorm.DB, inboundId int, name string) error {
	clients, err := (&ClientService{}).ListForInbound(tx, inboundId)
	if err != nil {
		return err
	}
	if list := hysteriaAuthsWithoutRoute(clients); list != "" {
		return fmt.Errorf("a Hysteria client carries its exit country in its auth, which must be a UUID or 32 hex characters; on %s: regenerate the auth of %s", name, list)
	}
	return nil
}

// refuseUnroutableHysteriaAuths is the same promise kept from the client side:
// once the inbound has an exit, no client may arrive with an auth no route fits.
func refuseUnroutableHysteriaAuths(inbound *model.Inbound, clients []model.Client) error {
	if inbound.Protocol != model.Hysteria {
		return nil
	}
	list := hysteriaAuthsWithoutRoute(clients)
	if list == "" {
		return nil
	}
	var exits int64
	if err := database.GetDB().Model(&model.Host{}).
		Where("inbound_id = ? AND COALESCE(exit_iface, '') <> ''", inbound.Id).Count(&exits).Error; err != nil {
		return err
	}
	if exits == 0 {
		return nil
	}
	return fmt.Errorf("%s has an exit country, which a Hysteria client carries in its auth: the auth of %s must be a UUID or 32 hex characters", inboundName(inbound), list)
}

// injectExitRoutes adds an outbound per exit and a rule per inbound and route,
// last, so the operator's rules, private and bittorrent blocks too, decide first.
func injectExitRoutes(cfg *xray.Config, inbounds []*model.Inbound, hosts []*model.Host) {
	var psi psiphonExits
	if len(sidecarPsiphonExits(inbounds)) > 0 ||
		slices.ContainsFunc(hosts, func(h *model.Host) bool { return strings.HasPrefix(h.Exit, psiphonExitPrefix) }) {
		psi = loadPsiphonExits()
	}
	injectExitRoutesWith(cfg, inbounds, hosts, psi)
}

// psiphonPoolExits is the Psiphon countries one inbound's pools leave through: a cisco
// inbound's locations or OpenVPN's exit, none while it is disabled or on a node.
func psiphonPoolExits(ib *model.Inbound) []string {
	if ib == nil || !ib.Enable || ib.NodeID != nil || (ib.Protocol != model.Cisco && ib.Protocol != model.OpenVPN) {
		return nil
	}
	var out []string
	for _, exit := range settingsExits(ib.Settings) {
		if _, ok := psiphonCode(exit); ok && !slices.Contains(out, exit) {
			out = append(out, exit)
		}
	}
	slices.Sort(out)
	return out
}

// sidecarPsiphonExits is every Psiphon country a cisco or OpenVPN pool leaves through.
func sidecarPsiphonExits(inbounds []*model.Inbound) []string {
	var out []string
	for _, ib := range inbounds {
		for _, exit := range psiphonPoolExits(ib) {
			if !slices.Contains(out, exit) {
				out = append(out, exit)
			}
		}
	}
	slices.Sort(out)
	return out
}

func psiphonRedirectTag(exit string) string { return "redir-" + exit }

// routeOnly off, so a sniffed name is what Psiphon dials, as measured.
const psiphonRedirectSniffing = `{"enabled":true,"destOverride":["http","tls"],"routeOnly":false}`

// psiphonRedirectInbound takes a country's cisco and OpenVPN pools, whose TCP the kernel
// redirects to port; 0.0.0.0, since a redirect lands on the address of the pool's device.
func psiphonRedirectInbound(exit string, port int) xray.InboundConfig {
	return xray.InboundConfig{
		Listen:      json_util.RawMessage(`"0.0.0.0"`),
		Port:        port,
		Protocol:    "dokodemo-door",
		Settings:    json_util.RawMessage(`{"network":"tcp","followRedirect":true}`),
		Sniffing:    json_util.RawMessage(psiphonRedirectSniffing),
		Tag:         psiphonRedirectTag(exit),
		PsiphonExit: exit,
	}
}

// psiphonRedirectPorts is the port each Psiphon redirect inbound a config opens takes its
// country's pools on, as the sidecars send them there.
func psiphonRedirectPorts(cfg *xray.Config) map[string]int {
	out := map[string]int{}
	if cfg == nil {
		return out
	}
	for _, ib := range cfg.InboundConfigs {
		if ib.PsiphonExit != "" {
			out[ib.PsiphonExit] = ib.Port
		}
	}
	return out
}

// What a start does with the redirect ports, as variables so a test can stand in; the
// sidecars are sent round off the core's lock, as the ten-second jobs would do it.
var (
	guardRedirectPorts  = relay.GuardPsiphonRedirects
	recordRedirectPorts = relay.SetPsiphonRedirects
	reassertSidecars    = func() {
		go func() { ocserv.GetManager().ReassertRoutes(); openvpn.GetManager().ReassertRoutes() }()
	}
)

// runWithPsiphonRedirects guards the redirect ports cfg opens, runs the core on it, and only
// once it runs there sends the pools to them; ran false, a hot apply refused, leaves them.
func runWithPsiphonRedirects(cfg *xray.Config, run func() (ran bool, err error)) (bool, error) {
	ports := psiphonRedirectPorts(cfg)
	if err := guardRedirectPorts(ports); err != nil {
		logger.Warning("exit routes: the Psiphon redirect ports are not all guarded:", err)
	}
	ran, err := run()
	if err != nil || !ran {
		return ran, err
	}
	changed, err := recordRedirectPorts(ports)
	if err != nil {
		logger.Warning("exit routes: the Psiphon redirect ports are not all guarded:", err)
	}
	// Without this a pool on a country just opened waits for the next ten-second pass.
	if changed {
		reassertSidecars()
	}
	return true, nil
}

// redirectPortTaken reports whether TCP port is already taken, by an inbound of the config,
// one a sidecar serves outside it, or the panel: the core could not bind it, nor start.
func redirectPortTaken(cfg *xray.Config, inbounds []*model.Inbound, panelPorts []int, port int) bool {
	return slices.Contains(panelPorts, port) ||
		slices.ContainsFunc(inbounds, func(ib *model.Inbound) bool {
			return ib.Enable && ib.NodeID == nil && claimedOn(portClaims(ib), port)&transportTCP != 0
		}) ||
		slices.ContainsFunc(cfg.InboundConfigs, func(ib xray.InboundConfig) bool {
			return ib.Port == port && configTransports(&ib)&transportTCP != 0
		})
}

func injectExitRoutesWith(cfg *xray.Config, inbounds []*model.Inbound, hosts []*model.Host, psi psiphonExits) {
	local := make(map[int]*model.Inbound, len(inbounds))
	for _, ib := range inbounds {
		if ib.Enable && ib.NodeID == nil && exitKindOf(ib.Protocol) != exitNone {
			local[ib.Id] = ib
		}
	}
	type key struct{ tag, route string }
	seen := map[key]bool{}
	var newRules []any
	var exits []string
	warped := map[string]bool{}
	psiphonRouted := false
	for _, h := range hosts {
		ib := local[h.InboundId]
		if ib == nil || h.IsDisabled || h.Exit == "" {
			continue
		}
		match := map[string]any{"type": "field", "inboundTag": []any{ib.Tag}}
		k := key{tag: ib.Tag}
		if exitKindOf(ib.Protocol) == exitByRoute {
			if h.VlessRoute == "" {
				continue
			}
			k.route = h.VlessRoute
			match["vlessRoute"] = h.VlessRoute
		}
		if seen[k] {
			continue
		}
		var rules []map[string]any
		if code, ok := psiphonCode(h.Exit); ok {
			psiphonRouted = true
			if psi.socks[code] == 0 {
				// Blocked, not skipped: with no rule the route falls to the default
				// outbound and leaves from this server's own address.
				logger.Warning("exit routes: Psiphon ", code, " is not enabled, so host ", h.Remark, "'s route is blocked")
				seen[k] = true
				newRules = append(newRules, ruleFrom(match, map[string]any{"outboundTag": psiphonBlockedTag}))
				continue
			}
			var warp bool
			rules, warp = psi.routeRules(match, h.Exit)
			warped[h.Exit] = warped[h.Exit] || warp
		} else {
			rules = []map[string]any{ruleFrom(match, map[string]any{"outboundTag": ExitOutboundTag(h.Exit)})}
		}
		seen[k] = true
		for _, r := range rules {
			newRules = append(newRules, r)
		}
		if !slices.Contains(exits, h.Exit) {
			exits = append(exits, h.Exit)
		}
	}
	var redirects []xray.InboundConfig
	for _, exit := range sidecarPsiphonExits(inbounds) {
		code, _ := psiphonCode(exit)
		tag := psiphonRedirectTag(exit)
		port := psiphonRedirectPort(psi.socks[code])
		// No redirect inbound leaves the pools at their blackhole: held closed.
		switch {
		case psi.socks[code] == 0:
			logger.Warning("exit routes: Psiphon ", code, " is not enabled, so the cisco and OpenVPN pools leaving through it are held closed")
			continue
		case inboundTagTaken(cfg, tag):
			logger.Warning("exit routes: an inbound is already tagged ", tag, ", so the cisco and OpenVPN pools leaving through Psiphon ", code, " are held closed")
			continue
		case redirectPortTaken(cfg, inbounds, psi.panelPorts, port):
			logger.Warning("exit routes: an inbound or the panel already takes TCP port ", port, ", where Psiphon ", code, "'s cisco and OpenVPN pools would be redirected, so they are held closed")
			continue
		}
		rules, warp := psi.routeRules(map[string]any{"type": "field", "inboundTag": []any{tag}}, exit)
		for _, r := range rules {
			newRules = append(newRules, r)
		}
		warped[exit] = warped[exit] || warp
		if !slices.Contains(exits, exit) {
			exits = append(exits, exit)
		}
		psiphonRouted = true
		redirects = append(redirects, psiphonRedirectInbound(exit, port))
	}
	if len(newRules) == 0 {
		return
	}

	var outbounds []any
	if len(cfg.OutboundConfigs) > 0 {
		if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
			logger.Warning("exit routes: outbounds section is unparsable, skipping injection:", err)
			return
		}
	}
	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("exit routes: routing section is unparsable, skipping injection:", err)
			return
		}
	}
	taken := map[string]bool{}
	for _, o := range outbounds {
		if m, ok := o.(map[string]any); ok {
			if t, ok := m["tag"].(string); ok {
				taken[t] = true
			}
		}
	}
	addOutbound := func(o map[string]any) {
		tag, _ := o["tag"].(string)
		if taken[tag] {
			logger.Warning("exit routes: the template already has an outbound tagged ", tag, "; its routes use that one")
			return
		}
		taken[tag] = true
		outbounds = append(outbounds, o)
	}
	for _, iface := range exits {
		if code, ok := psiphonCode(iface); ok {
			addOutbound(psiphonSocksOutbound(ExitOutboundTag(iface), psi.socks[code]))
			if warped[iface] {
				addOutbound(psi.warpOutbound(ExitOutboundTag(iface)))
			}
			continue
		}
		if taken[ExitOutboundTag(iface)] {
			logger.Warning("exit routes: the template already has an outbound tagged ", ExitOutboundTag(iface), "; its routes use that one")
			continue
		}
		id, _ := relay.InterfaceID(iface)
		src, err := relay.XrayExitSource(id)
		if err != nil {
			logger.Warning("exit routes:", err)
			continue
		}
		// ForceIPv4: the exit's tunnel is IPv4 only, so nothing may fall back
		// to this server's own IPv6 and leak its address.
		outbounds = append(outbounds, map[string]any{
			"tag":         ExitOutboundTag(iface),
			"protocol":    "freedom",
			"sendThrough": src,
			"settings":    map[string]any{"targetStrategy": "ForceIPv4"},
		})
	}
	// A Psiphon route's own UDP rules name these two; a template that renamed
	// them would otherwise fail to load.
	if psiphonRouted {
		if !taken[psiphonDirectTag] {
			addOutbound(map[string]any{"tag": psiphonDirectTag, "protocol": "freedom"})
		}
		if !taken[psiphonBlockedTag] {
			addOutbound(map[string]any{"tag": psiphonBlockedTag, "protocol": "blackhole"})
		}
	}
	rules, _ := routing["rules"].([]any)
	routing["rules"] = append(rules, newRules...)

	newOutbounds, err := json.MarshalIndent(outbounds, "", "  ")
	if err != nil {
		logger.Warning("exit routes: failed to rebuild outbounds, skipping injection:", err)
		return
	}
	newRouting, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("exit routes: failed to rebuild routing, skipping injection:", err)
		return
	}
	cfg.OutboundConfigs = json_util.RawMessage(newOutbounds)
	cfg.RouterConfig = json_util.RawMessage(newRouting)
	// Only now: a redirect inbound without its rules would send its pools out of the
	// default outbound, from this server's own address.
	cfg.InboundConfigs = append(cfg.InboundConfigs, redirects...)
}

func inboundTagTaken(cfg *xray.Config, tag string) bool {
	return slices.ContainsFunc(cfg.InboundConfigs, func(ib xray.InboundConfig) bool { return ib.Tag == tag })
}

// ruleFrom is a routing rule matching what match matches, with fields of its own.
func ruleFrom(match, fields map[string]any) map[string]any {
	rule := maps.Clone(match)
	maps.Copy(rule, fields)
	return rule
}

const (
	psiphonDirectTag  = "direct"
	psiphonBlockedTag = "blocked"
	// WARP over MASQUE answers behind this name; HTTP/2, since QUIC is UDP and a
	// Psiphon tunnel carries TCP only.
	warpMasqueSNI = "consumer-masque.cloudflareclient.com"
)

// psiphonExits is what the generator needs of the Psiphon countries: each enabled
// one's SOCKS port, and the WARP device that may ride behind them (nil: none).
type psiphonExits struct {
	socks     map[string]int
	warpMode  string
	warpSites []string
	warp      *psiphonWarpReg
	// panelPorts is where the panel and its subscription server listen, outside Xray.
	panelPorts []int
}

// routeRules is one Psiphon route's rules in order: DNS leaves from here, other UDP
// is refused so QUIC falls back to TCP, then the WARP sites, then everything else.
func (p psiphonExits) routeRules(match map[string]any, exit string) (rules []map[string]any, warp bool) {
	tag := ExitOutboundTag(exit)
	rules = []map[string]any{
		ruleFrom(match, map[string]any{"network": "udp", "port": "53", "outboundTag": psiphonDirectTag}),
		ruleFrom(match, map[string]any{"network": "udp", "outboundTag": psiphonBlockedTag}),
	}
	last := tag
	if p.warp != nil {
		switch p.warpMode {
		case psiphonWarpSites:
			if len(p.warpSites) > 0 {
				rules = append(rules, ruleFrom(match, map[string]any{"domain": p.warpSites, "outboundTag": warpOutboundTag(tag)}))
				warp = true
			}
		case psiphonWarpAll:
			last, warp = warpOutboundTag(tag), true
		}
	}
	return append(rules, ruleFrom(match, map[string]any{"outboundTag": last})), warp
}

func warpOutboundTag(exitTag string) string { return exitTag + "-warp" }

func psiphonSocksOutbound(tag string, port int) map[string]any {
	return map[string]any{
		"tag":      tag,
		"protocol": "socks",
		"settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port}}},
	}
}

// warpOutbound is WARP dialled through one country's Psiphon outbound, so the
// sites it carries still see that country.
func (p psiphonExits) warpOutbound(exitTag string) map[string]any {
	return map[string]any{
		"tag":      warpOutboundTag(exitTag),
		"protocol": "masque",
		"settings": map[string]any{"address": p.warp.Endpoint, "port": 443},
		"streamSettings": map[string]any{
			"network":     "masque",
			"security":    "tls",
			"tlsSettings": map[string]any{"serverName": warpMasqueSNI, "alpn": []any{"h2"}},
			"masqueSettings": map[string]any{"warp": map[string]any{
				"privateKey": p.warp.PrivateKey,
				"publicKey":  p.warp.PublicKey,
				"address":    p.warp.Address,
			}},
			"sockopt": map[string]any{"dialerProxy": exitTag},
		},
	}
}

// HostsUseExit reports whether any of the given host groups leaves through an
// exit, so only those edits make Xray reload.
func (s *HostService) HostsUseExit(groupIds ...string) bool {
	if len(groupIds) == 0 {
		return false
	}
	var n int64
	database.GetDB().Model(&model.Host{}).Where("group_id IN ? AND exit_iface <> ''", groupIds).Count(&n)
	return n > 0
}
