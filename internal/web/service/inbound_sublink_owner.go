package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// GetAllInboundLinksForOwner lists subscription links for the caller's own
// clients. Listing every client on their inbounds instead handed a reseller
// working credentials for the customers of whoever else shares that inbound.
// ownerId 0 is the unscoped path and keeps the whole-inbound behaviour.
func (s *InboundService) GetAllInboundLinksForOwner(host string, ownerId int) ([]string, error) {
	provider := subLinkProvider()
	if provider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	if ownerId <= 0 {
		return s.GetAllInboundLinks(host, 0)
	}
	inbounds, err := s.GetInbounds(ownerId)
	if err != nil {
		return nil, err
	}
	inboundIds := make([]int, 0, len(inbounds))
	for _, inbound := range inbounds {
		inboundIds = append(inboundIds, inbound.Id)
	}
	byInbound, err := ownedEmailsByInbound(ownerId, inboundIds)
	if err != nil {
		return nil, err
	}

	links := []string{}
	for _, inbound := range inbounds {
		for _, email := range byInbound[inbound.Id] {
			links = append(links, provider.LinksForClient(host, inbound, email)...)
		}
	}
	return links, nil
}

// GetInboundClientLinks is each client's links and tunnel configs on one
// inbound; a reseller sharing it gets only their own clients, as above.
func (s *InboundService) GetInboundClientLinks(host string, inboundId, ownerId int) ([]InboundClientLinks, error) {
	provider := subLinkProvider()
	if provider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	inbound, err := s.GetInbound(inboundId)
	if err != nil {
		return nil, err
	}
	var emails []string
	if ownerId > 0 {
		byInbound, err := ownedEmailsByInbound(ownerId, []int{inbound.Id})
		if err != nil {
			return nil, err
		}
		if emails = byInbound[inbound.Id]; len(emails) == 0 {
			return []InboundClientLinks{}, nil
		}
	}
	entries := []InboundClientLinks{}
	return append(entries, provider.InboundClientLinks(host, inbound, emails)...), nil
}

// ownedEmailsByInbound is, for each of inboundIds, the emails of ownerId's own clients on it.
func ownedEmailsByInbound(ownerId int, inboundIds []int) (map[int][]string, error) {
	type pair struct {
		InboundId int
		Email     string
	}
	var pairs []pair
	err := database.GetDB().Table("client_inbounds AS ci").
		Select("ci.inbound_id AS inbound_id, c.email AS email").
		Joins("JOIN clients AS c ON c.id = ci.client_id").
		Where("c.admin_id = ? AND ci.inbound_id IN ?", ownerId, inboundIds).
		Order("ci.inbound_id ASC, c.email ASC").
		Scan(&pairs).Error
	if err != nil {
		return nil, err
	}
	byInbound := make(map[int][]string, len(pairs))
	for _, p := range pairs {
		byInbound[p.InboundId] = append(byInbound[p.InboundId], p.Email)
	}
	return byInbound, nil
}
