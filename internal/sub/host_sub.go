package sub

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// hostEndpoints loads an inbound's enabled hosts for the given subscription
// format ("raw"|"json"|"clash") and returns them as externalProxy-shaped maps so
// the existing per-format renderers can fan out one link/proxy per host. Returns
// nil when the inbound has no applicable host — the caller then uses the legacy
// inbound/externalProxy path, preserving byte-identical output for zero-host
// inbounds.
func (s *SubService) hostEndpoints(inbound *model.Inbound, format string) []map[string]any {
	var hosts []*model.Host
	if err := database.GetDB().
		Where("inbound_id = ? AND is_disabled = ?", inbound.Id, false).
		Order("sort_order asc, id asc").
		Find(&hosts).Error; err != nil {
		logger.Warning("SubService - hostEndpoints:", err)
		return nil
	}
	if len(hosts) == 0 {
		return nil
	}
	defaultDest := s.resolveInboundAddress(inbound)
	countries := hostCountries(hosts)
	eps := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		if slices.Contains(h.ExcludeFromSubTypes, format) {
			continue
		}
		ep := hostToExternalProxyMap(h, defaultDest, inbound.Port)
		if c := countries[h.Id]; c != "" {
			ep["country"] = c
		}
		eps = append(eps, ep)
	}
	return eps
}

// localCountryLabel is this server's own flag and country; a variable so a test
// can stand in for the location lookup.
var localCountryLabel = func() string {
	if h, ok := relay.KnownHere(); ok {
		return relay.LabelFor(h.Country, h.CountryCode)
	}
	return ""
}

// hostCountries labels every host of an inbound that offers countries: an exit's
// own label, or this server's for a host that leaves here. Nil when none does.
func hostCountries(hosts []*model.Host) map[int]string {
	var exitIDs []int
	for _, h := range hosts {
		if id, ok := relay.InterfaceID(h.Exit); ok {
			exitIDs = append(exitIDs, id)
		}
	}
	if len(exitIDs) == 0 {
		return nil
	}
	labels := relayLabels(exitIDs)
	local := localCountryLabel()
	out := make(map[int]string, len(hosts))
	for _, h := range hosts {
		if id, ok := relay.InterfaceID(h.Exit); ok {
			out[h.Id] = labels[id]
		} else {
			out[h.Id] = local
		}
	}
	return out
}

// relayLabels is what a customer reads for each exit: its label, else its name.
func relayLabels(ids []int) map[int]string {
	var relays []model.Relay
	if err := database.GetDB().Where("id IN ?", ids).Find(&relays).Error; err != nil {
		logger.Warning("SubService - relayLabels:", err)
	}
	labels := make(map[int]string, len(relays))
	for _, r := range relays {
		labels[r.Id] = strings.TrimSpace(r.Label)
		if labels[r.Id] == "" {
			labels[r.Id] = r.Name
		}
	}
	return labels
}

// withCountry puts a host's country first in its config name. A name showing
// the flag stays as it is; one already spelling the country gains the flag only.
func withCountry(remark, country string) string {
	country = strings.TrimSpace(country)
	flag, name := relay.SplitLabel(country)
	switch {
	case country == "", flag != "" && strings.Contains(remark, flag):
		return remark
	case name != "" && strings.Contains(strings.ToLower(remark), strings.ToLower(name)):
		if flag == "" {
			return remark
		}
		return flag + " " + remark
	case strings.TrimSpace(remark) == "":
		return country
	}
	return country + " " + remark
}

// hostToExternalProxyMap projects a Host onto the externalProxy entry shape the
// raw/json/clash renderers already consume. Address/port fall back to the
// inbound's own when the host leaves them blank (override-only host).
func hostToExternalProxyMap(h *model.Host, defaultDest string, defaultPort int) map[string]any {
	dest := h.Address
	if dest == "" {
		dest = defaultDest
	}
	port := h.Port
	if port == 0 {
		port = defaultPort
	}
	ep := map[string]any{
		"forceTls": hostSecurityToForceTls(h.Security),
		"dest":     dest,
		"port":     float64(port),
		"remark":   h.Remark,
		// Marks this as a host (not a legacy externalProxy) entry so host-only
		// behaviors (e.g. reality SNI/fp override) apply without touching the
		// legacy externalProxy path. Not emitted into output.
		"isHost": true,
	}
	sni := h.Sni
	if h.OverrideSniFromAddress {
		sni = dest
	}
	if !h.KeepSniBlank && sni != "" {
		ep["sni"] = sni
	}
	if h.Fingerprint != "" {
		ep["fingerprint"] = h.Fingerprint
	}
	if h.CipherSuites != "" {
		ep["cipherSuites"] = h.CipherSuites
	}
	if len(h.Alpn) > 0 {
		ep["alpn"] = stringsToAnySlice(h.Alpn)
	}
	if len(h.PinnedPeerCertSha256) > 0 {
		ep["pinnedPeerCertSha256"] = stringsToAnySlice(h.PinnedPeerCertSha256)
	}
	if h.EchConfigList != "" {
		ep["echConfigList"] = h.EchConfigList
	}
	if h.VerifyPeerCertByName != "" {
		ep["verifyPeerCertByName"] = h.VerifyPeerCertByName
	}
	if h.AllowInsecure {
		ep["allowInsecure"] = true
	}
	if h.HostHeader != "" {
		ep["hostHeader"] = h.HostHeader
	}
	if h.Path != "" {
		ep["path"] = h.Path
	}
	if h.MihomoIpVersion != "" {
		ep["mihomoIpVersion"] = h.MihomoIpVersion
	}
	if h.SockoptParams != "" {
		ep["sockoptParams"] = h.SockoptParams
	}
	if h.MuxParams != "" {
		ep["muxParams"] = h.MuxParams
	}
	if h.FinalMask != "" {
		ep["finalMask"] = h.FinalMask
	}
	if h.VlessRoute != "" {
		ep["vlessRoute"] = h.VlessRoute
	}
	if h.ServerDescription != "" {
		ep["serverDescription"] = h.ServerDescription
	}
	return ep
}

// hostMuxOverride returns a host's muxParams when it is valid JSON, else "".
// Used to override the JSON outbound's mux for that host.
func hostMuxOverride(ep map[string]any) string {
	mp, ok := ep["muxParams"].(string)
	if ok && mp != "" && json.Valid([]byte(mp)) {
		return mp
	}
	return ""
}

// applyHostStreamOverrides injects a host's free-JSON stream overrides into the
// per-host stream the JSON/Clash renderers build: sockoptParams (re-added since
// the base stream strips sockopt) and finalMask. No-op for legacy externalProxy
// entries (which never carry these keys), so existing output is unchanged.
func applyHostStreamOverrides(ep map[string]any, stream map[string]any) {
	if hh, ok := ep["hostHeader"].(string); ok && hh != "" {
		for _, key := range []string{"wsSettings", "httpupgradeSettings", "xhttpSettings"} {
			if ts, ok := stream[key].(map[string]any); ok && ts != nil {
				ts["host"] = hh
			}
		}
	}
	if p, ok := ep["path"].(string); ok && p != "" {
		for _, key := range []string{"wsSettings", "httpupgradeSettings", "xhttpSettings"} {
			if ts, ok := stream[key].(map[string]any); ok && ts != nil {
				ts["path"] = p
			}
		}
	}
	if sp, ok := ep["sockoptParams"].(string); ok && sp != "" {
		var sockopt map[string]any
		if json.Unmarshal([]byte(sp), &sockopt) == nil && len(sockopt) > 0 {
			stream["sockopt"] = sockopt
		}
	}
	// Host finalmask: merge the host's masks into the stream's finalmask (the
	// JSON renderer consumes streamSettings["finalmask"]; clash ignores it).
	if fm, ok := ep["finalMask"].(string); ok && fm != "" {
		var masks map[string]any
		if json.Unmarshal([]byte(fm), &masks) == nil && len(masks) > 0 {
			merged := mergeFinalMask(stream["finalmask"], masks)
			if len(merged) > 0 {
				stream["finalmask"] = merged
			}
		}
	}
	// Reality SNI override (host only): the stream is already in client form, and xray
	// refuses a reality client carrying the server-side serverNames list (#6690).
	if isHostEndpoint(ep) {
		if sec, _ := stream["security"].(string); sec == "reality" {
			if rs, ok := stream["realitySettings"].(map[string]any); ok && rs != nil {
				if sni, ok := externalProxySNI(ep); ok {
					rs["serverName"] = sni
				}
			}
		}
	}
}

// hostSecurityToForceTls maps Host.Security onto the externalProxy forceTls
// vocabulary. "reality"/"same"/"" all keep the inbound's base security ("same")
// — reality parameters can only come from the inbound itself.
func hostSecurityToForceTls(security string) string {
	switch security {
	case "tls", "none":
		return security
	default:
		return "same"
	}
}

func stringsToAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// injectExternalProxy rewrites the inbound's StreamSettings so its externalProxy
// array is exactly eps. Host endpoints win over any legacy externalProxy.
func injectExternalProxy(inbound *model.Inbound, eps []map[string]any) {
	stream := unmarshalStreamSettings(inbound.StreamSettings)
	if stream == nil {
		stream = map[string]any{}
	}
	arr := make([]any, len(eps))
	for i := range eps {
		arr[i] = eps[i]
	}
	stream["externalProxy"] = arr
	if b, err := json.Marshal(stream); err == nil {
		inbound.StreamSettings = string(b)
	}
}

// linkFromHosts renders a (possibly multi-line) raw link for one client using
// the given host endpoints. It renders ONLY the hosts: an empty eps yields ""
// (no legacy fallback) — the caller decides when to take the legacy path. That
// separation is what makes the zero-hosts fallback mutation-testable.
func (s *SubService) linkFromHosts(inbound *model.Inbound, client model.Client, eps []map[string]any) string {
	if len(eps) == 0 {
		return ""
	}
	stream := unmarshalStreamSettings(inbound.StreamSettings)
	transport, _ := stream["network"].(string)
	// Clone each ep before expanding its remark template: the eps slice is
	// shared across all clients of this inbound, so the rendered (per-client)
	// remark must not leak into the next client's links.
	rendered := make([]map[string]any, len(eps))
	for i, ep := range eps {
		cp := maps.Clone(ep)
		s.renderHostRemark(inbound, client, cp, transport)
		rendered[i] = cp
	}
	clone := *inbound
	injectExternalProxy(&clone, rendered)
	return s.GetLink(&clone, client.Email)
}

// renderHostRemark expands a host endpoint's {{VAR}} remark template for one
// client in place and marks it final, so the downstream link/proxy/config
// renderers emit it verbatim (via endpointRemark) instead of re-composing it.
// No-op for non-host endpoints (legacy externalProxy / synthetic default), so
// their output stays byte-identical.
func (s *SubService) renderHostRemark(inbound *model.Inbound, client model.Client, ep map[string]any, transport string) {
	if !isHostEndpoint(ep) {
		return
	}
	tmpl, _ := ep["remark"].(string)
	country, _ := ep["country"].(string)
	ep["remark"] = withCountry(s.genHostRemark(inbound, client, tmpl, transport), country)
	ep["remarkFinal"] = true
}

// endpointRemark names an endpoint's entry: a host's remark renderHostRemark expanded, else
// genRemark's; an endpoint of a front's IPv6/IPv4 pair appends its epRemarkSuffix to either.
func (s *SubService) endpointRemark(inbound *model.Inbound, email string, ep map[string]any, transport string) string {
	if ep == nil {
		return s.genRemark(inbound, email, "", transport)
	}
	suffix, _ := ep[epRemarkSuffix].(string)
	if final, _ := ep["remarkFinal"].(bool); final {
		r, _ := ep["remark"].(string)
		return withRemarkSuffix(r, suffix)
	}
	extra, _ := ep["remark"].(string)
	return withRemarkSuffix(s.genRemark(inbound, email, extra, transport), suffix)
}

// applyEndpointHostPath overrides the transport host header / path for a host
// endpoint. It is a no-op for legacy externalProxy entries (which never carry
// hostHeader/path) and only replaces keys the transport already emits, so it
// cannot add spurious params to e.g. a tcp link.
func applyEndpointHostPath(e ShareEndpoint, params map[string]string) {
	if e.ep == nil {
		return
	}
	if h, ok := e.ep["hostHeader"].(string); ok && h != "" {
		if _, exists := params["host"]; exists {
			params["host"] = h
		}
	}
	if p, ok := e.ep["path"].(string); ok && p != "" {
		if _, exists := params["path"]; exists {
			params["path"] = p
		}
	}
}

// isHostEndpoint reports whether ep was synthesized from a Host (vs a legacy
// externalProxy entry), so host-only overrides stay off the legacy path.
func isHostEndpoint(ep map[string]any) bool {
	v, _ := ep["isHost"].(bool)
	return v
}

// applyEndpointRealityParams overrides a reality link's SNI + fingerprint from a
// host (reality's pbk/sid are inherited from the inbound, so they aren't touched).
// Host-only: legacy externalProxy reality links are unchanged.
func applyEndpointRealityParams(e ShareEndpoint, params map[string]string, security string) {
	if security != "reality" || e.ep == nil || !isHostEndpoint(e.ep) {
		return
	}
	if sni, ok := externalProxySNI(e.ep); ok {
		params["sni"] = sni
	}
	if fp, ok := e.ep["fingerprint"].(string); ok && fp != "" {
		params["fp"] = fp
	}
}

// applyEndpointAllowInsecure adds allowInsecure=1 to a TLS/Reality link when the
// host opts into skipping cert verification. No-op for legacy externalProxy
// entries (which never carry the key) and for plaintext (none) endpoints.
func applyEndpointAllowInsecure(e ShareEndpoint, params map[string]string, security string) {
	if e.ep == nil || security == "none" {
		return
	}
	if ai, ok := e.ep["allowInsecure"].(bool); ok && ai {
		params["allowInsecure"] = "1"
	}
}

// applyEndpointFinalMask merges a host's Final Mask into the raw link's fm
// param, mirroring the applyHostStreamOverrides merge on the JSON/Clash path.
func applyEndpointFinalMask(e ShareEndpoint, params map[string]string) {
	if merged, ok := endpointFinalMask(e, params["fm"]); ok {
		params["fm"] = merged
	}
}

// applyEndpointFinalMaskObj is applyEndpointFinalMask for the VMess object form.
func applyEndpointFinalMaskObj(e ShareEndpoint, obj map[string]any) {
	baseFm, _ := obj["fm"].(string)
	if merged, ok := endpointFinalMask(e, baseFm); ok {
		obj["fm"] = merged
	}
}

func endpointFinalMask(e ShareEndpoint, baseFm string) (string, bool) {
	if e.ep == nil {
		return "", false
	}
	fm, ok := e.ep["finalMask"].(string)
	if !ok || fm == "" {
		return "", false
	}
	var masks map[string]any
	if json.Unmarshal([]byte(fm), &masks) != nil || len(masks) == 0 {
		return "", false
	}
	var base any
	if baseFm != "" {
		var baseMap map[string]any
		if json.Unmarshal([]byte(baseFm), &baseMap) == nil {
			base = baseMap
		}
	}
	return marshalFinalMask(mergeFinalMask(base, masks))
}

// applyEndpointHostPathObj is applyEndpointHostPath for the VMess object form.
func applyEndpointHostPathObj(e ShareEndpoint, obj map[string]any) {
	if e.ep == nil {
		return
	}
	if h, ok := e.ep["hostHeader"].(string); ok && h != "" {
		if _, exists := obj["host"]; exists {
			obj["host"] = h
		}
	}
	if p, ok := e.ep["path"].(string); ok && p != "" {
		if _, exists := obj["path"]; exists {
			obj["path"] = p
		}
	}
}
