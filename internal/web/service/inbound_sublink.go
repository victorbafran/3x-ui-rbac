package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// TunnelConfig is one WireGuard .conf, AmneziaWG .conf or TUIC Clash YAML of a
// client, for one address it dials; Family is "v6"/"v4" behind an IPv6 entry server.
type TunnelConfig struct {
	InboundId int    `json:"inboundId" example:"3"`
	Protocol  string `json:"protocol" example:"wireguard"`
	Email     string `json:"email" example:"alice"`
	Address   string `json:"address" example:"[2001:db8::1]"`
	Port      int    `json:"port" example:"51820"`
	Family    string `json:"family" example:"v6"`
	Endpoint  string `json:"endpoint" example:"[2001:db8::1]:51820"`
	Remark    string `json:"remark" example:"WG - alice · IPv6"`
	Text      string `json:"text" example:"[Interface]\nPrivateKey = ...\n[Peer]\nEndpoint = [2001:db8::1]:51820"`
}

// InboundClientLinks is one client's share links and tunnel configs on one inbound.
type InboundClientLinks struct {
	Email   string         `json:"email" example:"alice"`
	Links   []string       `json:"links" example:"[\"vless://uuid@host:443?security=reality#alice\"]"`
	Configs []TunnelConfig `json:"configs"`
}

type SubLinkProvider interface {
	SubLinksForSubId(host, subId string) ([]string, error)
	LinksForClient(host string, inbound *model.Inbound, email string) []string
	LinksForInbounds(host string, inbounds []*model.Inbound) []string
	TunnelConfigsForClient(host string, inbounds []*model.Inbound, email string) []TunnelConfig
	// InboundClientLinks lists the inbound's clients in settings order; nil emails means all of them.
	InboundClientLinks(host string, inbound *model.Inbound, emails []string) []InboundClientLinks
}

var registeredSubLinkProvider SubLinkProvider

func RegisterSubLinkProvider(p SubLinkProvider) {
	registeredSubLinkProvider = p
}

func (s *InboundService) GetSubLinks(host, subId string) ([]string, error) {
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	return registeredSubLinkProvider.SubLinksForSubId(host, subId)
}

func (s *InboundService) GetAllInboundLinks(host string, userId int) ([]string, error) {
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	inbounds, err := s.GetInbounds(userId)
	if err != nil {
		return nil, err
	}
	return registeredSubLinkProvider.LinksForInbounds(host, inbounds), nil
}

func (s *InboundService) GetAllClientLinks(host string, email string) ([]string, error) {
	if email == "" {
		return nil, common.NewError("client email is required")
	}
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	rec, err := s.clientService.GetRecordByEmail(nil, email)
	if err != nil {
		return nil, err
	}
	inboundIds, err := s.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return nil, err
	}
	var links []string
	for _, ibId := range inboundIds {
		inbound, getErr := s.GetInbound(ibId)
		if getErr != nil {
			return nil, getErr
		}
		links = append(links, registeredSubLinkProvider.LinksForClient(host, inbound, email)...)
	}
	return links, nil
}

// GetTunnelConfigs is every WireGuard, AmneziaWG and TUIC config of one client
// record across the inbounds it is attached to; other protocols give none.
func (s *InboundService) GetTunnelConfigs(host string, clientId int) ([]TunnelConfig, error) {
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	rec, err := s.clientService.GetByID(clientId)
	if err != nil {
		return nil, err
	}
	inboundIds, err := s.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return nil, err
	}
	inbounds := make([]*model.Inbound, 0, len(inboundIds))
	for _, ibId := range inboundIds {
		inbound, getErr := s.GetInbound(ibId)
		if getErr != nil {
			return nil, getErr
		}
		inbounds = append(inbounds, inbound)
	}
	configs := []TunnelConfig{}
	return append(configs, registeredSubLinkProvider.TunnelConfigsForClient(host, inbounds, rec.Email)...), nil
}
