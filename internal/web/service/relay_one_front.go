package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// a variable so a test can stand in for DNS.
var resolveHost = net.DefaultResolver.LookupIPAddr

// a name that does not resolve in time is compared by its spelling alone.
const sameMachineTimeout = 5 * time.Second

// refuseASecondFront keeps one machine to one entry server: each front opens
// every inbound's port, so a second one there would fail to open any of them.
func (s *RelayService) refuseASecondFront(r *model.Relay) error {
	if r.Role != model.RelayFront {
		return nil
	}
	var fronts []model.Relay
	q := database.GetDB().Where("role = ?", model.RelayFront)
	if r.Id > 0 {
		q = q.Where("id != ?", r.Id)
	}
	if err := q.Find(&fronts).Error; err != nil {
		return err
	}
	if len(fronts) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), sameMachineTimeout)
	defer cancel()
	// The address the panel dials, never the public name: several fronts behind
	// one round-robin name is a sound setup.
	mine := machineAddresses(ctx, r.Address)
	for _, f := range fronts {
		for a := range machineAddresses(ctx, f.Address) {
			if mine[a] {
				return fmt.Errorf("%s is already the entry server %s, on %s; one machine carries one tunnel, so remove %s first to move it to another tool",
					r.Address, f.Name, frontTool(f), f.Name)
			}
		}
	}
	return nil
}

// machineAddresses is the address normalised plus, for a name, the IPs it
// resolves to; a failed lookup leaves only the spelling.
func machineAddresses(ctx context.Context, address string) map[string]bool {
	host := strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(address), "[]"), "."))
	out := map[string]bool{}
	if host == "" {
		return out
	}
	if ip := net.ParseIP(host); ip != nil {
		out[ip.String()] = true
		return out
	}
	out[host] = true
	if addrs, err := resolveHost(ctx, host); err == nil {
		for _, a := range addrs {
			out[a.IP.String()] = true
		}
	}
	return out
}
