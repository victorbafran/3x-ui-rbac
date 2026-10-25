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
	"github.com/mhsanaei/3x-ui/v3/internal/util/certreload"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// Appended by default to the two configs a front set to "both" hands out per address.
const (
	remarkSuffixV6 = " · IPv6"
	remarkSuffixV4 = " · IPv4"
	// epRemarkSuffix rides on an endpoint map to endpointRemark; never emitted.
	epRemarkSuffix = "remarkSuffix"
	// epFamily rides the same way: "v6" or "v4" on a front's endpoint, for the panel.
	epFamily = "ipFamily"
)

// Families of a front's two renderings, as the panel's configs carry them.
const (
	familyV6 = "v6"
	familyV4 = "v4"
)

// familyMarks tell a "both" front's two configs apart by subRemarkIPFamily, the
// IPv6 one's first; an unknown mode is the default, the family's name.
func familyMarks(mode string) (mark6, mark4 string) {
	switch mode {
	case entity.SubRemarkIPFamilyNumber:
		return " 1", " 2"
	case entity.SubRemarkIPFamilyOff:
		return "", ""
	}
	return remarkSuffixV6, remarkSuffixV4
}

// familyLabel is a mark standing on its own, as over a cisco login.
func familyLabel(mark string) string {
	return strings.TrimLeft(mark, " ·")
}

// front6 is an entry server whose customers are given its IPv6 address.
type front6 struct {
	names    []string // every spelling of the front, either family, by addressKey
	names4   []string // Address and PublicName, by addressKey
	address6 string   // what its IPv6 configs dial: PublicName6, else Address6 bracketed
	name6    string   // PublicName6, a name whose only record is AAAA; "" for none
	address4 string   // what an IPv4 twin dials instead of an IPv6: PublicName, else Address
	both     bool
}

// ipv4Address is what addr's IPv4 twin dials: addr itself when it already is the
// front's IPv4 name or address, else the front's IPv4 one.
func (f *front6) ipv4Address(addr string) string {
	if slices.Contains(f.names4, addressKey(addr)) {
		return addr
	}
	return f.address4
}

// tlsName6 is the IPv6 configs' TLS name: the IPv6 name when the inbound's own
// certificate covers it or address4 gives none, else address4's, as the twin's.
func (f *front6) tlsName6(inbound *model.Inbound, address4 string) string {
	if f.name6 != "" && (tlsNameOf(address4) == "" || certreload.Covers(ownCertFile(inbound), f.name6)) {
		return f.name6
	}
	return address4
}

// twinTLSName is the name addr's IPv4 twin dialling address4 must write in: addr's
// when address4 is a bare IP, which gives none; "" when address4 gives its own.
func twinTLSName(addr, address4 string) string {
	if tlsNameOf(address4) != "" {
		return ""
	}
	return tlsNameOf(addr)
}

// ipVariant is how one rendering of an inbound differs from its plain one.
type ipVariant struct {
	address string // replaces what resolveInboundAddress returns; "" keeps it
	suffix  string // appended to every remark genRemark builds
	family  string // familyV6 or familyV4 for a front's rendering, "" for the plain one
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
	if name6 := strings.TrimSuffix(strings.TrimSpace(r.PublicName6), "."); name6 != "" {
		f.address6, f.name6 = name6, name6
	}
	if f.address4 = strings.TrimSpace(r.PublicName); f.address4 == "" {
		f.address4 = strings.TrimSpace(r.Address)
	}
	for _, name := range []string{r.Address, r.PublicName} {
		if key := addressKey(name); key != "" {
			f.names4 = append(f.names4, key)
		}
	}
	f.names = append(slices.Clone(f.names4), ip.String())
	if f.name6 != "" {
		f.names = append(f.names, addressKey(f.name6))
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
	// No config of their own, but JSON makes a stray one a pair would double:
	// ciscoLoginsFor and openvpnProfileFor follow the front instead.
	if inbound.Protocol == model.Cisco || inbound.Protocol == model.OpenVPN {
		return nil
	}
	mark6, mark4 := familyMarks(s.remarkIPFamily)
	if len(eps) == 0 {
		address := s.resolveInboundAddress(inbound)
		front := s.frontNamed(address)
		if front == nil {
			return nil
		}
		address4 := front.ipv4Address(address)
		v6 := frontUnit{inbound: withTLSName(inbound, front.tlsName6(inbound, address4)), variant: ipVariant{address: front.address6, family: familyV6}}
		if !front.both {
			return []frontUnit{v6}
		}
		v6.variant.suffix = mark6
		v4 := frontUnit{inbound: inbound, variant: ipVariant{suffix: mark4, family: familyV4}}
		if address4 != address {
			v4.variant.address = address4
		}
		if name := twinTLSName(address, address4); name != "" {
			v4.inbound = withTLSName(inbound, name)
		}
		return []frontUnit{v6, v4}
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
	// One copy per spelling and family: each carries the TLS name tlsName6 or
	// twinTLSName gives its address.
	copies, copies4 := map[string]*model.Inbound{}, map[string]*model.Inbound{}
	for _, ep := range eps {
		dest, _ := ep["dest"].(string)
		front := s.frontNamed(dest)
		if front == nil {
			add(inbound, ep)
			continue
		}
		dest4 := front.ipv4Address(dest)
		v6Inbound, seen := copies[dest]
		if !seen {
			v6Inbound = withTLSName(inbound, front.tlsName6(inbound, dest4))
			copies[dest] = v6Inbound
		}
		v6 := frontEndpoint(ep, front.address6, front.tlsName6(inbound, dest4))
		if !front.both {
			add(v6Inbound, v6)
			continue
		}
		add(v6Inbound, withMark(v6, mark6))
		v4Inbound, name4 := inbound, twinTLSName(dest, dest4)
		if name4 != "" {
			if v4Inbound = copies4[dest]; v4Inbound == nil {
				v4Inbound = withTLSName(inbound, name4)
				copies4[dest] = v4Inbound
			}
		}
		add(v4Inbound, withMark(ipv4Endpoint(ep, dest4, name4), mark4))
	}
	if len(copies) == 0 {
		return nil
	}
	return units
}

// frontEndpoint is ep's IPv6 copy, dialling address6. An endpoint that forces TLS
// sends tlsName's name, since an IPv6 literal gives none.
func frontEndpoint(ep map[string]any, address6, tlsName string) map[string]any {
	v6 := maps.Clone(ep)
	v6["dest"] = address6
	v6[epFamily] = familyV6
	// It dials one family; a host's "ipv4" would make mihomo refuse to dial it.
	delete(v6, "mihomoIpVersion")
	withForcedTLSName(v6, tlsNameOf(tlsName))
	return v6
}

// ipv4Endpoint is ep's IPv4 twin, dialling dest4 and forcing twinTLSName's name.
// A family pin set for the IPv6 ep named would make mihomo refuse dest4, so it goes.
func ipv4Endpoint(ep map[string]any, dest4, tlsName string) map[string]any {
	v4 := maps.Clone(ep)
	v4[epFamily] = familyV4
	if dest, _ := ep["dest"].(string); dest != dest4 {
		v4["dest"] = dest4
		delete(v4, "mihomoIpVersion")
	}
	withForcedTLSName(v4, tlsName)
	return v4
}

// withForcedTLSName gives an endpoint forcing TLS name as its SNI, unless it
// names one itself; the stream it forces TLS onto has no name to give.
func withForcedTLSName(ep map[string]any, name string) {
	if forceTls, _ := ep["forceTls"].(string); forceTls != "tls" || name == "" {
		return
	}
	if _, has := externalProxySNI(ep); !has {
		ep["sni"] = name
	}
}

// withMark puts the mark endpointRemark appends on ep; "off" leaves the name alone.
func withMark(ep map[string]any, mark string) map[string]any {
	if mark != "" {
		ep[epRemarkSuffix] = mark
	}
	return ep
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
