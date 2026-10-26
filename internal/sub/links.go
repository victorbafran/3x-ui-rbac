package sub

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type LinkProvider struct {
	settingService service.SettingService
}

func NewLinkProvider() *LinkProvider {
	return &LinkProvider{}
}

func (p *LinkProvider) build(host string) *SubService {
	remarkTemplate, _ := p.settingService.GetRemarkTemplate()
	svc := NewSubService(remarkTemplate)
	svc.PrepareForRequest(host)
	return svc
}

func (p *LinkProvider) SubLinksForSubId(host, subId string) ([]string, error) {
	svc := p.build(host)
	links, _, _, _, err := svc.GetSubs(subId, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, splitLinkLines(l)...)
	}
	return out, nil
}

func (p *LinkProvider) LinksForClient(host string, inbound *model.Inbound, email string) []string {
	svc := p.build(host)
	svc.projectThroughFallbackMaster(inbound)
	baseEps := svc.hostEndpoints(inbound, "raw")
	if client, ok := svc.clientForLink(inbound, email); ok {
		link, closed := svc.clientRawLink(inbound, client, baseEps, svc.frontUnits(inbound, rawEndpoints(inbound, baseEps)))
		if closed {
			return nil
		}
		if link != "" {
			return splitLinkLines(link)
		}
	}
	return splitLinkLines(svc.GetLink(inbound, email))
}

func (p *LinkProvider) LinksForInbounds(host string, inbounds []*model.Inbound) []string {
	svc := p.build(host)
	var out []string
	for _, inbound := range inbounds {
		out = append(out, svc.inboundLinks(inbound)...)
	}
	return out
}

// TunnelConfigsForClient is one client's WireGuard, AmneziaWG and TUIC configs on
// inbounds, all on one request so they share its fronts and settings.
func (p *LinkProvider) TunnelConfigsForClient(host string, inbounds []*model.Inbound, email string) []service.TunnelConfig {
	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", email).First(&rec).Error; err != nil {
		return nil
	}
	svc := p.build(host)
	var out []service.TunnelConfig
	for _, inbound := range inbounds {
		svc.projectThroughFallbackMaster(inbound)
		plan := svc.tunnelPlan(inbound)
		if plan == nil {
			continue
		}
		if client, ok := svc.tunnelIdentities(inbound, []model.Client{*rec.ToClient()})[strings.ToLower(rec.Email)]; ok {
			out = append(out, plan.configs(client)...)
		}
	}
	return out
}

func (p *LinkProvider) InboundClientLinks(host string, inbound *model.Inbound, emails []string) []service.InboundClientLinks {
	return p.build(host).inboundClientLinks(inbound, emails)
}

func splitLinkLines(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
