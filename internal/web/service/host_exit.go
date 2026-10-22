package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// A host with an exit is a country a customer picks: its route rides the UUID's
// third group, which Xray masks out to find the user and reads to route.

// ExitOutboundTag is the Xray outbound that leaves through one exit server.
func ExitOutboundTag(iface string) string { return "exit-" + iface }

// settleHostExit checks a host's exit and gives it the route that selects it:
// the exit's relay id, unless the operator typed one.
func settleHostExit(tx *gorm.DB, groupId string, req *entity.HostGroup) error {
	req.Exit = strings.TrimSpace(req.Exit)
	if req.Exit == "" {
		return nil
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
	req.VlessRoute = strings.TrimSpace(req.VlessRoute)
	if req.VlessRoute == "" {
		req.VlessRoute = strconv.Itoa(id)
	}
	if n, err := strconv.Atoi(req.VlessRoute); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("VLESS route %q is not a number from 0 to 65535", req.VlessRoute)
	}

	var inbounds []model.Inbound
	if err := tx.Where("id IN ?", req.InboundIds).Find(&inbounds).Error; err != nil {
		return err
	}
	for _, ib := range inbounds {
		name := ib.Remark
		if name == "" {
			name = ib.Tag
		}
		if ib.Protocol != model.VLESS {
			return fmt.Errorf("an exit country needs a VLESS inbound, and %s is %s", name, ib.Protocol)
		}
		if ib.NodeID != nil {
			return fmt.Errorf("%s runs on a node, whose traffic never reaches this server's exits", name)
		}
	}

	// One route picks one exit on an inbound; the same route to two would be
	// decided by rule order, silently.
	var clash model.Host
	err := tx.Where("inbound_id IN ? AND vless_route = ? AND exit_iface <> '' AND exit_iface <> ? AND group_id <> ?",
		req.InboundIds, req.VlessRoute, req.Exit, groupId).First(&clash).Error
	if err == nil {
		return fmt.Errorf("VLESS route %s already leaves through %s in host %q; give this one another route", req.VlessRoute, clash.Exit, clash.Remark)
	}
	return nil
}

// injectExitRoutes adds an outbound per exit and a rule per inbound and route,
// last, so the operator's rules, private and bittorrent blocks too, decide first.
func injectExitRoutes(cfg *xray.Config, inbounds []*model.Inbound, hosts []*model.Host) {
	local := make(map[int]*model.Inbound, len(inbounds))
	for _, ib := range inbounds {
		if ib.Enable && ib.NodeID == nil && ib.Protocol == model.VLESS {
			local[ib.Id] = ib
		}
	}
	type key struct{ tag, route string }
	seen := map[key]bool{}
	var newRules []any
	var exits []string
	for _, h := range hosts {
		ib := local[h.InboundId]
		if ib == nil || h.IsDisabled || h.Exit == "" || h.VlessRoute == "" {
			continue
		}
		k := key{ib.Tag, h.VlessRoute}
		if seen[k] {
			continue
		}
		seen[k] = true
		newRules = append(newRules, map[string]any{
			"type":        "field",
			"inboundTag":  []any{ib.Tag},
			"vlessRoute":  h.VlessRoute,
			"outboundTag": ExitOutboundTag(h.Exit),
		})
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
