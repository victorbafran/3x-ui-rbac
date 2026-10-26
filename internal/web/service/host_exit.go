package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
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
	id, ok := relay.InterfaceID(req.Exit)
	if !ok {
		return fmt.Errorf("%q is not an exit server", req.Exit)
	}
	var exit model.Relay
	if err := tx.Where("id = ? AND role = ?", id, model.RelayExit).First(&exit).Error; err != nil {
		return fmt.Errorf("there is no exit server %s", req.Exit)
	}
	if _, err := relay.XrayExitSource(id); err != nil {
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
		req.VlessRoute = strconv.Itoa(id)
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
	for _, h := range hosts {
		ib := local[h.InboundId]
		if ib == nil || h.IsDisabled || h.Exit == "" {
			continue
		}
		rule := map[string]any{
			"type":        "field",
			"inboundTag":  []any{ib.Tag},
			"outboundTag": ExitOutboundTag(h.Exit),
		}
		k := key{tag: ib.Tag}
		if exitKindOf(ib.Protocol) == exitByRoute {
			if h.VlessRoute == "" {
				continue
			}
			k.route = h.VlessRoute
			rule["vlessRoute"] = h.VlessRoute
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		newRules = append(newRules, rule)
		if !slices.Contains(exits, h.Exit) {
			exits = append(exits, h.Exit)
		}
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
	for _, iface := range exits {
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
