package service

import (
	"net"
	"strings"
	"unicode"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// validateSidecarIPv6 refuses a cisco or OpenVPN inbound's own IPv6 that would
// hand customers an address they cannot dial. A name is not looked up here.
func validateSidecarIPv6(protocol model.Protocol, settings string) error {
	if protocol != model.Cisco && protocol != model.OpenVPN {
		return nil
	}
	own := sidecar.IPv6SettingsOf(settings)
	if _, err := relay.ParseIPFamily(own.IPFamily); err != nil {
		return common.NewErrorf("ipFamily: %v", err)
	}
	if own.Address6 == "" {
		if own.ServesIPv6() {
			return common.NewErrorf("ipFamily %q needs an IPv6 address or a name for it in address6", own.IPFamily)
		}
		return nil
	}
	if strings.Contains(own.Address6, ":") || net.ParseIP(own.Address6) != nil {
		if _, err := relay.NormalizeAddress6(own.Address6); err != nil {
			return common.NewErrorf("address6: %v", err)
		}
		return nil
	}
	if strings.ContainsFunc(own.Address6, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return common.NewErrorf("address6: %q is not a host name", own.Address6)
	}
	return nil
}
