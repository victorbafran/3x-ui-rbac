package sub

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tuic"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// tunnelTarget is one address a client's tunnel config dials, and what names it.
type tunnelTarget struct {
	inbound    *model.Inbound // the rendering's copy, with a front's TLS name written in
	address    string         // an IPv6 literal bracketed
	port       int
	hostRemark string // the host's or externalProxy entry's own remark
	mark       string // subRemarkIPFamily's mark on one of a front's pair
	family     string
	endpoint   string         // tells sibling configs apart; "" for the inbound's own address
	ep         map[string]any // nil for the inbound's own address
}

// tunnelTargets walks an inbound's addresses as its links do: its hosts of format,
// else its externalProxy, else its own address, a front's IPv6/IPv4 pair in place.
func (s *SubService) tunnelTargets(inbound *model.Inbound, format string) []tunnelTarget {
	eps := rawEndpoints(inbound, s.hostEndpoints(inbound, format))
	units := s.frontUnits(inbound, eps)
	if units == nil {
		units = []frontUnit{{inbound: inbound, eps: eps}}
	}
	var out []tunnelTarget
	for _, u := range units {
		if len(u.eps) == 0 {
			t := tunnelTarget{inbound: u.inbound, port: u.inbound.Port, mark: u.variant.suffix, family: u.variant.family}
			s.withVariant(u.variant, func() { t.address = bracketIPv6(s.resolveInboundAddress(u.inbound)) })
			if t.family != "" {
				t.endpoint = joinHostPort(t.address, t.port)
			}
			out = append(out, t)
			continue
		}
		for _, ep := range u.eps {
			e := externalProxyToEndpoint(ep)
			t := tunnelTarget{inbound: u.inbound, address: bracketIPv6(e.Address), port: e.Port, hostRemark: e.Remark, ep: ep}
			t.mark, _ = ep[epRemarkSuffix].(string)
			t.family, _ = ep[epFamily].(string)
			t.endpoint = joinHostPort(t.address, t.port)
			out = append(out, t)
		}
	}
	distinctEndpoints(out)
	return out
}

// distinctEndpoints tells apart targets dialling one endpoint, as hosts naming one
// front do: by host remark, then by order among those still alike.
func distinctEndpoints(targets []tunnelTarget) {
	alike := repeatedEndpoints(targets)
	for i, t := range targets {
		if alike[t.endpoint] && t.hostRemark != "" {
			targets[i].endpoint += " (" + t.hostRemark + ")"
		}
	}
	alike = repeatedEndpoints(targets)
	order := map[string]int{}
	for i, t := range targets {
		if alike[t.endpoint] {
			order[t.endpoint]++
			targets[i].endpoint = fmt.Sprintf("%s #%d", t.endpoint, order[t.endpoint])
		}
	}
}

// repeatedEndpoints are the endpoints more than one target dials.
func repeatedEndpoints(targets []tunnelTarget) map[string]bool {
	count := map[string]int{}
	for _, t := range targets {
		if t.endpoint != "" {
			count[t.endpoint]++
		}
	}
	repeated := map[string]bool{}
	for endpoint, n := range count {
		if n > 1 {
			repeated[endpoint] = true
		}
	}
	return repeated
}

// tunnelPlan is what every client's configs on one inbound share, worked out once:
// where they dial and the server's half of the text.
type tunnelPlan struct {
	inbound *model.Inbound
	targets []tunnelTarget
	render  func(t tunnelTarget, c model.Client) (remark, text string)
}

// tunnelPlan is nil for an inbound with no downloadable client config.
func (s *SubService) tunnelPlan(inbound *model.Inbound) *tunnelPlan {
	label := inboundLabel(inbound)
	p := &tunnelPlan{inbound: inbound}
	switch inbound.Protocol {
	case model.WireGuard:
		publicKey, mtu, dns := service.InboundWireguardHints(string(inbound.Protocol), inbound.Settings)
		p.targets = s.tunnelTargets(inbound, "raw")
		p.render = func(t tunnelTarget, c model.Client) (string, string) {
			remark := withRemarkSuffix(joinRemark(label, t.hostRemark, c.Email, c.Comment), t.mark)
			return remark, wireguardPanelConf(c, publicKey, mtu, dns, joinHostPort(t.address, t.port), remark)
		}
	case model.AmneziaWG:
		var parsed amneziawg.InboundSettings
		if json.Unmarshal([]byte(inbound.Settings), &parsed) != nil || parsed.Server == nil {
			return nil
		}
		p.targets = s.tunnelTargets(inbound, "raw")
		p.render = func(t tunnelTarget, c model.Client) (string, string) {
			remark := withRemarkSuffix(joinRemark(label, t.hostRemark, c.Email, c.Comment), t.mark)
			return remark, amneziaWGPanelConf(parsed.Server, c, joinHostPort(t.address, t.port), remark)
		}
	case model.TUIC:
		p.targets = s.tunnelTargets(inbound, "clash")
		servers := make(map[*model.Inbound]tuic.Instance, len(p.targets))
		for _, t := range p.targets {
			if _, seen := servers[t.inbound]; seen {
				continue
			}
			inst, ok := tuic.InstanceFromInbound(t.inbound)
			if !ok {
				return nil
			}
			servers[t.inbound] = inst
		}
		p.render = func(t tunnelTarget, c model.Client) (string, string) {
			remark := withRemarkSuffix(joinRemark(label, t.hostRemark, c.Email), t.mark)
			return remark, tuicPanelYAML(servers[t.inbound], t, c, remark)
		}
	default:
		return nil
	}
	return p
}

// configs is one client's config per target; none without the key it needs,
// which the subscription's own link for that client also refuses.
func (p *tunnelPlan) configs(c model.Client) []service.TunnelConfig {
	if p.inbound.Protocol == model.TUIC {
		if c.ID == "" || c.Password == "" {
			return nil
		}
	} else if c.PrivateKey == "" {
		return nil
	}
	out := make([]service.TunnelConfig, 0, len(p.targets))
	for _, t := range p.targets {
		remark, text := p.render(t, c)
		if text == "" {
			continue
		}
		out = append(out, service.TunnelConfig{
			InboundId: p.inbound.Id,
			Protocol:  string(p.inbound.Protocol),
			Email:     c.Email,
			Address:   t.address,
			Port:      t.port,
			Family:    t.family,
			Endpoint:  t.endpoint,
			Remark:    remark,
			Text:      text,
		})
	}
	return out
}

// inboundClientLinks is inboundLinks per client, with each one's tunnel configs;
// emails, unless nil, keeps only those clients.
func (s *SubService) inboundClientLinks(inbound *model.Inbound, emails []string) []service.InboundClientLinks {
	out := []service.InboundClientLinks{}
	r, ok := s.renderInbound(inbound)
	if !ok {
		return out
	}
	var wanted map[string]bool
	if emails != nil {
		wanted = make(map[string]bool, len(emails))
		for _, e := range emails {
			wanted[strings.ToLower(e)] = true
		}
	}
	var identities map[string]model.Client
	plan := s.tunnelPlan(inbound)
	if plan != nil {
		if table, err := s.inboundService.ListClientsForInbound(inbound.Id); err == nil {
			identities = s.tunnelIdentities(inbound, table)
		}
	}
	for _, client := range r.clients {
		key := strings.ToLower(client.Email)
		if wanted != nil && !wanted[key] {
			continue
		}
		entry := service.InboundClientLinks{Email: client.Email, Links: []string{}, Configs: []service.TunnelConfig{}}
		entry.Links = append(entry.Links, r.links(s, client)...)
		if identity, ok := identities[key]; ok {
			entry.Configs = append(entry.Configs, plan.configs(identity)...)
		}
		out = append(out, entry)
	}
	return out
}

// tunnelIdentities keys clients by lowercased email as their configs see them: on
// WireGuard and AmneziaWG with this inbound's own keys and address (#6641).
func (s *SubService) tunnelIdentities(inbound *model.Inbound, clients []model.Client) map[string]model.Client {
	if inbound.Protocol == model.WireGuard || inbound.Protocol == model.AmneziaWG {
		overlaid, err := s.overlayInboundTunnelIdentity(inbound, clients)
		if err != nil {
			return nil
		}
		clients = overlaid
	}
	byEmail := make(map[string]model.Client, len(clients))
	for _, c := range clients {
		key := strings.ToLower(c.Email)
		if _, dup := byEmail[key]; !dup {
			byEmail[key] = c
		}
	}
	return byEmail
}

// inboundLabel is an inbound's name on screen: its remark, else its tag.
func inboundLabel(inbound *model.Inbound) string {
	if remark := strings.TrimSpace(inbound.Remark); remark != "" {
		return remark
	}
	return strings.TrimSpace(inbound.Tag)
}

func joinRemark(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " - ")
}

// bracketIPv6 writes a bare IPv6 literal the way an address field holds one.
func bracketIPv6(addr string) string {
	if strings.Contains(addr, ":") && net.ParseIP(addr) != nil {
		return "[" + addr + "]"
	}
	return addr
}

func isIPv6Literal(addr string) bool {
	return strings.Contains(addr, ":") && net.ParseIP(strings.Trim(addr, "[]")) != nil
}

// tunnelAddress is the client's tunnel address on this inbound, else fallback.
func tunnelAddress(c model.Client, fallback string) string {
	if joined := strings.Join(c.AllowedIPs, ","); joined != "" {
		return joined
	}
	return fallback
}

// confText joins a config's lines; "" when one holds a line break, which would add
// a line of its own to the file a customer imports: a rogue PostUp, a YAML key.
func confText(lines []string) string {
	for _, line := range lines {
		if strings.ContainsAny(line, "\r\n") {
			return ""
		}
	}
	return strings.Join(lines, "\n")
}

// wireguardPanelConf is the WireGuard .conf the panel offers a client, line for
// line what the clients page built in the browser before; "" as confText refuses.
func wireguardPanelConf(c model.Client, publicKey string, mtu int, dns, endpoint, remark string) string {
	if dns == "" {
		dns = "1.1.1.1, 1.0.0.1"
	}
	lines := []string{"[Interface]", "PrivateKey = " + c.PrivateKey, "Address = " + tunnelAddress(c, "10.0.0.2/32"), "DNS = " + dns}
	if mtu > 0 {
		lines = append(lines, "MTU = "+strconv.Itoa(mtu))
	}
	return confText(append(lines, peerLines(publicKey, c, endpoint, remark)...))
}

// amneziaWGPanelConf is the AmneziaWG .conf the panel offers, as the clients page
// built it; "" as confText refuses.
func amneziaWGPanelConf(server *amneziawg.ServerSettings, c model.Client, endpoint, remark string) string {
	lines := []string{"[Interface]", "PrivateKey = " + c.PrivateKey, "Address = " + tunnelAddress(c, "10.8.1.2/32")}
	var dns []string
	for _, d := range []string{server.PrimaryDNS, server.SecondaryDNS} {
		if strings.TrimSpace(d) != "" {
			dns = append(dns, d)
		}
	}
	if len(dns) > 0 {
		lines = append(lines, "DNS = "+strings.Join(dns, ", "))
	}
	lines = append(lines,
		fmt.Sprintf("MTU = %d", amneziawg.EffectiveMTU(server.MTU, server.S4)),
		fmt.Sprintf("Jc = %d", server.Jc),
		fmt.Sprintf("Jmin = %d", server.Jmin),
		fmt.Sprintf("Jmax = %d", server.Jmax),
		fmt.Sprintf("S1 = %d", server.S1),
		fmt.Sprintf("S2 = %d", server.S2),
	)
	if server.S3 != 0 {
		lines = append(lines, fmt.Sprintf("S3 = %d", server.S3))
	}
	if server.S4 != 0 {
		lines = append(lines, fmt.Sprintf("S4 = %d", server.S4))
	}
	for i, h := range []string{server.H1, server.H2, server.H3, server.H4} {
		lines = append(lines, fmt.Sprintf("H%d = %s", i+1, amneziaWGHeaderOrDefault(h, strconv.Itoa(i+1))))
	}
	for i, v := range []string{server.I1, server.I2, server.I3, server.I4, server.I5} {
		if v != "" {
			lines = append(lines, fmt.Sprintf("I%d = %s", i+1, v))
		}
	}
	for _, p := range []struct{ key, v string }{
		{"HeaderProtectionKey", server.HeaderProtectionKey},
		{"ContentPaddingAddition", server.ContentPaddingAddition},
		{"RekeyAfterTime", server.RekeyAfterTime},
		{"RekeyTimeout", server.RekeyTimeout},
		{"RejectAfterTime", server.RejectAfterTime},
		{"KeepaliveTimeout", server.KeepaliveTimeout},
		{"MaxHandshakeAttempts", server.MaxHandshakeAttempts},
	} {
		if strings.TrimSpace(p.v) != "" {
			lines = append(lines, p.key+" = "+p.v)
		}
	}
	if server.RandomTrailers {
		lines = append(lines, "RandomTrailers = on")
	}
	if server.DisableCookies {
		lines = append(lines, "DisableCookies = on")
	}
	return confText(append(lines, peerLines(server.PublicKey, c, endpoint, remark)...))
}

// peerLines closes a .conf in wg-quick(8)'s peer order, the remark as a comment above it.
func peerLines(publicKey string, c model.Client, endpoint, remark string) []string {
	lines := []string{""}
	if remark != "" {
		lines = append(lines, "# "+remark)
	}
	lines = append(lines, "[Peer]", "PublicKey = "+publicKey)
	if c.PreSharedKey != "" {
		lines = append(lines, "PresharedKey = "+c.PreSharedKey)
	}
	lines = append(lines, "AllowedIPs = 0.0.0.0/0, ::/0", "Endpoint = "+endpoint)
	if ka := c.KeepAliveSeconds(); ka > 0 {
		lines = append(lines, "PersistentKeepalive = "+strconv.Itoa(ka))
	}
	return lines
}

// tuicPanelYAML is the TUIC Clash YAML the panel offers, as the clients page built it,
// "" as confText refuses; an IPv6 server is written bare and quoted, never as the SNI.
func tuicPanelYAML(inst tuic.Instance, t tunnelTarget, c model.Client, remark string) string {
	alpn, sni, insecure := inst.ALPN, inst.SNI, false
	if t.ep != nil {
		if v, ok := externalProxyALPN(t.ep["alpn"]); ok {
			alpn = strings.Split(v, ",")
		}
		if v, ok := externalProxySNI(t.ep); ok {
			sni = v
		}
		insecure, _ = t.ep["allowInsecure"].(bool)
	}
	server := t.address
	if isIPv6Literal(server) {
		server = jsonQuote(strings.Trim(server, "[]"))
	} else if sni == "" {
		sni = server
	}
	port := t.port
	if port == 0 {
		port = 8443
	}
	lines := []string{
		"# TUIC v5 Client Configuration (Clash / Mihomo / Clash Verge)",
		"# " + remark,
		"proxies:",
		"  - name: " + jsonQuote(remark),
		"    type: tuic",
		"    server: " + server,
		"    port: " + strconv.Itoa(port),
		"    uuid: " + c.ID,
		"    password: " + jsonQuote(c.Password),
		"    alpn:",
	}
	for _, a := range alpn {
		lines = append(lines, "      - "+a)
	}
	if sni != "" {
		if strings.ContainsAny(sni, `"'\#`) || strings.IndexFunc(sni, unicode.IsSpace) >= 0 {
			sni = jsonQuote(sni)
		}
		lines = append(lines, "    sni: "+sni)
	}
	lines = append(lines,
		"    congestion-controller: "+inst.CongestionControl,
		"    udp-relay-mode: "+inst.UDPRelayMode,
		"    reduce-rtt: "+strconv.FormatBool(inst.ZeroRTTHandshake),
	)
	if insecure {
		lines = append(lines, "    skip-cert-verify: true")
	}
	return confText(lines)
}

// jsonQuote is a YAML double-quoted scalar, which a JSON string is.
func jsonQuote(v string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}
