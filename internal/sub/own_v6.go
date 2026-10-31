package sub

import (
	"net"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// ownFront6 is a cisco or OpenVPN inbound's own IPv6, served by this server, in
// a front's shape; address4 is what its IPv4 customers dial. nil keeps IPv4.
func ownFront6(inbound *model.Inbound, address4 string) *front6 {
	own := sidecar.IPv6SettingsOf(inbound.Settings)
	if !own.ServesIPv6() || own.Address6 == "" {
		return nil
	}
	f := front6{address4: address4, both: own.IPFamily == relay.IPFamilyBoth}
	if key := addressKey(address4); key != "" {
		f.names4 = []string{key}
	}
	host := strings.Trim(own.Address6, "[]")
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return nil
		}
		f.address6 = "[" + ip.String() + "]"
		return &f
	}
	f.address6 = strings.TrimSuffix(host, ".")
	f.name6 = f.address6
	return &f
}

// ipv6Front is who hands out an inbound's IPv6: a front its address names,
// else the inbound itself; nil when its customers stay on IPv4.
func (s *SubService) ipv6Front(inbound *model.Inbound, address string) *front6 {
	if front := s.frontNamed(address); front != nil {
		return front
	}
	return ownFront6(inbound, address)
}
