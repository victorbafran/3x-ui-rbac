package sub

import (
	"encoding/json"
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tuic"
)

// Appended to the two configs a front set to "both" hands out per address.
const (
	remarkSuffixV6 = " · IPv6"
	remarkSuffixV4 = " · IPv4"
	// epRemarkSuffix rides on an endpoint map to endpointRemark; never emitted.
	epRemarkSuffix = "remarkSuffix"
)

// front6 is an entry server whose customers are given its IPv6 address.
type front6 struct {
	names    []string // Address and PublicName, spelled by addressKey
	address6 string   // bracketed, the shape a custom IPv6 share address has
	both     bool
}

// ipVariant is how one rendering of an inbound differs from its plain one.
type ipVariant struct {
	address string // replaces what resolveInboundAddress returns; "" keeps it
	suffix  string // appended to every remark genRemark builds
}

// frontUnit is one rendering of an inbound: a front's IPv6 one, or the plain one.
// eps nil renders the inbound's own address; otherwise exactly these endpoints.
type frontUnit struct {
	inbound *model.Inbound
	eps     []map[string]any
	variant ipVariant
}

// loadFronts6 reads the fronts that hand out IPv6, once per request.
func (s *SubService) loadFronts6() {
	s.fronts6 = nil
	var rows []model.Relay
	if err := database.GetDB().Where("enabled = ? AND role = ?", true, model.RelayFront).
		Order("id asc").Find(&rows).Error; err != nil {
		logger.Warning("subscription: load IPv6 fronts failed:", err)
		return
	}
	for _, r := range rows {
		if f, ok := frontFromRelay(r); ok {
			s.fronts6 = append(s.fronts6, f)
		}
	}
}

// frontFromRelay reads an enabled front for the subscription; false when its
// customers stay on IPv4, the default an empty IPFamily means.
func frontFromRelay(r model.Relay) (front6, bool) {
	if r.IPFamily != "v6" && r.IPFamily != "both" {
		return front6{}, false
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(r.Address6), "[]"))
	if ip == nil || ip.To4() != nil {
		return front6{}, false
	}
	f := front6{address6: "[" + ip.String() + "]", both: r.IPFamily == "both"}
	for _, name := range []string{r.Address, r.PublicName} {
		if key := addressKey(name); key != "" {
			f.names = append(f.names, key)
		}
	}
	return f, true
}

// addressKey is how two spellings of one address compare: case, brackets and
// a trailing dot do not make a different host.
func addressKey(addr string) string {
	addr = strings.TrimSuffix(strings.Trim(strings.TrimSpace(addr), "[]"), ".")
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return strings.ToLower(addr)
}

// frontNamed is the IPv6 front addr points at, or nil.
func (s *SubService) frontNamed(addr string) *front6 {
	key := addressKey(addr)
	if key == "" {
		return nil
	}
	for i := range s.fronts6 {
		if slices.Contains(s.fronts6[i].names, key) {
			return &s.fronts6[i]
		}
	}
	return nil
}

// frontUnits splits an inbound's rendering by the IPv6 fronts its addresses name;
// nil when none does, so the caller keeps its plain path. eps are its hosts or externalProxy.
func (s *SubService) frontUnits(inbound *model.Inbound, eps []map[string]any) []frontUnit {
	if len(s.fronts6) == 0 {
		return nil
	}
	// Cisco's certificate is for a name AnyConnect refuses as an IPv6 literal;
	// OpenVPN gets its IPv6 as a second remote in openvpnProfileFor instead.
	if inbound.Protocol == model.Cisco || inbound.Protocol == model.OpenVPN {
		return nil
	}
	if len(eps) == 0 {
		address := s.resolveInboundAddress(inbound)
		front := s.frontNamed(address)
		if front == nil {
			return nil
		}
		v6 := frontUnit{inbound: withTLSName(inbound, address), variant: ipVariant{address: front.address6}}
		if !front.both {
			return []frontUnit{v6}
		}
		v6.variant.suffix = remarkSuffixV6
		return []frontUnit{v6, {inbound: inbound, variant: ipVariant{suffix: remarkSuffixV4}}}
	}

	// Endpoints keep the operator's order, each IPv6 copy in its place and its
	// IPv4 one after it; a run sharing one inbound copy renders as one unit.
	var units []frontUnit
	add := func(ib *model.Inbound, ep map[string]any) {
		if n := len(units); n > 0 && units[n-1].inbound == ib {
			units[n-1].eps = append(units[n-1].eps, ep)
			return
		}
		units = append(units, frontUnit{inbound: ib, eps: []map[string]any{ep}})
	}
	// One copy per spelling: each carries the TLS name its own address gave.
	copies := map[string]*model.Inbound{}
	for _, ep := range eps {
		dest, _ := ep["dest"].(string)
		front := s.frontNamed(dest)
		if front == nil {
			add(inbound, ep)
			continue
		}
		v6Inbound, seen := copies[dest]
		if !seen {
			v6Inbound = withTLSName(inbound, dest)
			copies[dest] = v6Inbound
		}
		add(v6Inbound, frontEndpoint(ep, dest, front))
		if front.both {
			v4 := maps.Clone(ep)
			v4[epRemarkSuffix] = remarkSuffixV4
			add(inbound, v4)
		}
	}
	if len(copies) == 0 {
		return nil
	}
	return units
}

// frontEndpoint is ep's IPv6 copy. An endpoint that forces TLS keeps the name
// its address gave it, since an IPv6 literal gives none.
func frontEndpoint(ep map[string]any, dest string, front *front6) map[string]any {
	v6 := maps.Clone(ep)
	v6["dest"] = front.address6
	// A literal has one family; a host's "ipv4" would make mihomo refuse to dial it.
	delete(v6, "mihomoIpVersion")
	if front.both {
		v6[epRemarkSuffix] = remarkSuffixV6
	}
	if forceTls, _ := ep["forceTls"].(string); forceTls == "tls" {
		if _, has := externalProxySNI(ep); !has {
			if name := tlsNameOf(dest); name != "" {
				v6["sni"] = name
			}
		}
	}
	return v6
}

// tlsNameOf is the SNI a client derives from addr: the host name, none for an IP.
func tlsNameOf(addr string) string {
	name := strings.TrimSuffix(strings.TrimSpace(addr), ".")
	if name == "" || net.ParseIP(strings.Trim(name, "[]")) != nil {
		return ""
	}
	return name
}

// withTLSName copies inbound for an IPv6 rendering. A TLS that took its name from
// the address gets it written in: Iran drops a ClientHello without one.
func withTLSName(inbound *model.Inbound, address string) *model.Inbound {
	clone := *inbound
	name := tlsNameOf(address)
	if name == "" {
		return &clone
	}
	if inbound.Protocol == model.TUIC {
		var settings map[string]any
		if inst, ok := tuic.InstanceFromInbound(inbound); ok && inst.SNI == "" &&
			json.Unmarshal([]byte(inbound.Settings), &settings) == nil && settings != nil {
			settings["sni"] = name
			if out, err := json.Marshal(settings); err == nil {
				clone.Settings = string(out)
			}
		}
		return &clone
	}
	stream := unmarshalStreamSettings(inbound.StreamSettings)
	security, _ := stream["security"].(string)
	tlsSettings, _ := stream["tlsSettings"].(map[string]any)
	serverName, _ := tlsSettings["serverName"].(string)
	takesTLS := security == "tls" || inbound.Protocol == model.Hysteria
	if stream == nil || !takesTLS || strings.TrimSpace(serverName) != "" {
		return &clone
	}
	if tlsSettings == nil {
		tlsSettings = map[string]any{}
		stream["tlsSettings"] = tlsSettings
	}
	tlsSettings["serverName"] = name
	if out, err := json.Marshal(stream); err == nil {
		clone.StreamSettings = string(out)
	}
	return &clone
}

// streamEndpoints is an inbound's externalProxy entries; nil when it has none.
func streamEndpoints(inbound *model.Inbound) []map[string]any {
	raw, _ := unmarshalStreamSettings(inbound.StreamSettings)["externalProxy"].([]any)
	var eps []map[string]any
	for _, entry := range raw {
		if ep, ok := entry.(map[string]any); ok {
			eps = append(eps, ep)
		}
	}
	return eps
}

// rawEndpoints is what an inbound's raw links fan out over: its hosts, else its externalProxy.
func rawEndpoints(inbound *model.Inbound, hostEps []map[string]any) []map[string]any {
	if len(hostEps) > 0 {
		return hostEps
	}
	return streamEndpoints(inbound)
}

// withVariant runs render with v in force for resolveInboundAddress and genRemark.
func (s *SubService) withVariant(v ipVariant, render func()) {
	s.variant = v
	defer func() { s.variant = ipVariant{} }()
	render()
}

// withRemarkSuffix appends suffix; a remark that was empty becomes the suffix alone.
func withRemarkSuffix(remark, suffix string) string {
	if suffix == "" {
		return remark
	}
	if strings.TrimSpace(remark) == "" {
		return strings.TrimSpace(suffix)
	}
	return remark + suffix
}

// clientLink is one client's raw links on an inbound: through its hosts when it
// has them, and a front's IPv6 ones ahead of the rest.
func (s *SubService) clientLink(inbound *model.Inbound, client model.Client, hostEps []map[string]any, units []frontUnit) string {
	if units == nil {
		if len(hostEps) > 0 {
			return s.linkFromHosts(inbound, client, hostEps)
		}
		return s.GetLink(inbound, client.Email)
	}
	links := make([]string, 0, len(units))
	for _, u := range units {
		var link string
		s.withVariant(u.variant, func() {
			if u.eps != nil {
				link = s.linkFromHosts(u.inbound, client, u.eps)
			} else {
				link = s.GetLink(u.inbound, client.Email)
			}
		})
		if link != "" {
			links = append(links, link)
		}
	}
	return strings.Join(links, "\n")
}

// renderUnits renders each unit on its own copy of the inbound, its endpoints
// injected, for the JSON and Clash outputs that read them off the stream.
func renderUnits[T any](s *SubService, units []frontUnit, render func(*model.Inbound) []T) []T {
	var out []T
	for _, u := range units {
		ib := *u.inbound
		if u.eps != nil {
			injectExternalProxy(&ib, u.eps)
		}
		s.withVariant(u.variant, func() { out = append(out, render(&ib)...) })
	}
	return out
}
