package service

import (
	"context"
	"fmt"
	"net"
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
// commands, a 5 s dial and a 5 s lookup, never what a caller's job left over.
const frontIPv6Budget = 50 * time.Second

// Variables so a test can stand in for the front's SSH and for the network.
var (
	discoverFrontIPv6 = relay.DiscoverIPv6
	reachFrontIPv6    = relay.ReachIPv6
	checkFrontName6   = relay.Name6Problem
)

// fillFrontIPv6 settles a front's IPv6, its IPv6 name and its customers' family.
// An unchanged address and name keep what the panel last learned about them.
func fillFrontIPv6(r *model.Relay, req RelayRequest, base *model.Relay) error {
	if typed := strings.TrimSpace(req.Address6); typed != "" {
		address6, err := relay.NormalizeAddress6(typed)
		if err != nil {
			return fmt.Errorf("the front's IPv6: %w", err)
		}
		r.Address6 = address6
	}
	name6, err := normalizeFrontName(req.PublicName6)
	if err != nil {
		return fmt.Errorf("the IPv6 name %q is not a host name: %w", req.PublicName6, err)
	}
	if ip := net.ParseIP(strings.Trim(name6, "[]")); ip.To4() != nil {
		return fmt.Errorf("the IPv6 name %q is an IPv4 address; the front's IPv4 goes in its address or public name, and here a name whose only record is AAAA to its IPv6", req.PublicName6)
	} else if ip != nil {
		return fmt.Errorf("the IPv6 name %q is an address; the address goes in the IPv6 field, and here a name whose only record is AAAA to it", req.PublicName6)
	}
	r.PublicName6 = name6
	family, err := relay.ParseIPFamily(req.IPFamily)
	if err != nil {
		return err
	}
	r.IPFamily = family
	switch {
	case r.Address6 == "":
		r.Address6Error = ""
	case base != nil && base.Address6 == r.Address6 && base.PublicName6 == r.PublicName6:
		r.Address6Error = base.Address6Error
	default:
		r.Address6Error = address6NotChecked
	}
	return nil
}

// refreshFrontIPv6 reads a front's IPv6 off it when none is known, dials it from
// the panel and resolves its IPv6 name. Only address6_error hears the outcome.
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
			noteAddress6(r, "", err.Error())
			return
		}
		// Into a still-empty row only: an address typed meanwhile is the operator's.
		res := database.GetDB().Model(&model.Relay{}).Where("id = ? AND address6 = ?", r.Id, "").
			Updates(map[string]any{"address6": found, "address6_error": address6NotChecked})
		if res.Error != nil || res.RowsAffected == 0 {
			return
		}
		logger.Infof("front %s: its IPv6 is %s", r.Name, found)
		address6 = found
	}
	problems := []string{reachFrontIPv6(ctx, address6, s.frontIPv6Port(r))}
	if r.PublicName6 != "" {
		problems = append(problems, checkFrontName6(ctx, r.PublicName6, address6))
	}
	problem := strings.Join(slices.DeleteFunc(problems, func(p string) bool { return p == "" }), "; ")
	if problem != "" {
		logger.Warningf("front %s: IPv6 %s", r.Name, problem)
	}
	noteAddress6(r, address6, problem)
}

// noteAddress6 records a verdict against the address and IPv6 name it was
// about; an edit made since then owns the field.
func noteAddress6(r model.Relay, address6, problem string) {
	if err := database.GetDB().Model(&model.Relay{}).
		Where("id = ? AND address6 = ? AND public_name6 = ?", r.Id, address6, r.PublicName6).
		Update("address6_error", problem).Error; err != nil {
		logger.Warningf("relay %d: could not record what its IPv6 did: %v", r.Id, err)
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
