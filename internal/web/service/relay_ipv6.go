package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
)

// address6NotChecked marks an IPv6 the panel has not dialled yet; "" would
// say it answered.
const address6NotChecked = "not checked yet"

// frontIPv6Budget is one check's own clock: an SSH dial of up to 20 s, three
// commands and a 5 s dial, never what a caller's longer job left over.
const frontIPv6Budget = 45 * time.Second

// Variables so a test can stand in for the front's SSH and for the network.
var (
	discoverFrontIPv6 = relay.DiscoverIPv6
	reachFrontIPv6    = relay.ReachIPv6
)

// fillFrontIPv6 settles a front's IPv6 and its customers' family. An unchanged
// address keeps what the panel last learned about reaching it.
func fillFrontIPv6(r *model.Relay, req RelayRequest, base *model.Relay) error {
	if typed := strings.TrimSpace(req.Address6); typed != "" {
		address6, err := relay.NormalizeAddress6(typed)
		if err != nil {
			return fmt.Errorf("the front's IPv6: %w", err)
		}
		r.Address6 = address6
	}
	family, err := relay.ParseIPFamily(req.IPFamily)
	if err != nil {
		return err
	}
	r.IPFamily = family
	switch {
	case r.Address6 == "":
		r.Address6Error = ""
	case base != nil && base.Address6 == r.Address6:
		r.Address6Error = base.Address6Error
	default:
		r.Address6Error = address6NotChecked
	}
	return nil
}

// refreshFrontIPv6 reads a front's IPv6 off it when none is known, then dials it
// from the panel. Only address6_error hears the outcome: IPv4 and status never wait.
func (s *RelayService) refreshFrontIPv6(r model.Relay) {
	ctx, cancel := context.WithTimeout(context.Background(), frontIPv6Budget)
	defer cancel()
	address6 := r.Address6
	if address6 == "" {
		creds, err := frontCreds(r)
		found := ""
		if err == nil {
			found, err = discoverFrontIPv6(ctx, creds)
		}
		if err != nil {
			noteAddress6(r.Id, "", err.Error())
			return
		}
		// Into a still-empty row only: an address typed meanwhile is the operator's.
		res := database.GetDB().Model(&model.Relay{}).Where("id = ?", r.Id).
			Updates(map[string]any{"address6": found, "address6_error": address6NotChecked})
		if res.Error != nil || res.RowsAffected == 0 {
			return
		}
		logger.Infof("front %s: its IPv6 is %s", r.Name, found)
		address6 = found
	}
	problem := reachFrontIPv6(ctx, address6, s.frontIPv6Port(r))
	if problem != "" {
		logger.Warningf("front %s: IPv6 %s", r.Name, problem)
	}
	noteAddress6(r.Id, address6, problem)
}

// noteAddress6 records a verdict against the address it was about; an edit
// made since then owns the field.
func noteAddress6(id int, address6, problem string) {
	if err := database.GetDB().Model(&model.Relay{}).Where("id = ? AND address6 = ?", id, address6).
		Update("address6_error", problem).Error; err != nil {
		logger.Warningf("relay %d: could not record what its IPv6 did: %v", id, err)
	}
}

// frontIPv6Port is a customer port the front opens: never the tunnel's, which
// binds IPv4 only, and the ACME port only when nothing else is carried.
func (s *RelayService) frontIPv6Port(r model.Relay) int {
	pair := tunnel.Pair{Tool: frontTool(r), Transport: tunnel.Transport(r.Transport), TunnelPort: r.TunnelPort}
	plain, proxied, err := s.frontForwardedPorts(r.TunnelPort, tunnel.SecondTunnelPort(pair), r.SSHPort)
	if err != nil {
		return 0
	}
	ports := slices.Concat(plain, proxied)
	if i := slices.IndexFunc(ports, func(p int) bool { return p != tunnel.ACMEPort }); i >= 0 {
		return ports[i]
	}
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}
