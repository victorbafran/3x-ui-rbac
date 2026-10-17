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
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	if ownerId <= 0 {
		return s.GetAllInboundLinks(host, 0)
	}
	inbounds, err := s.GetInbounds(ownerId)
	if err != nil {
		return nil, err
	}
	type pair struct {
		InboundId int
		Email     string
	}
	var pairs []pair
	err = database.GetDB().Table("client_inbounds AS ci").
		Select("ci.inbound_id AS inbound_id, c.email AS email").
		Joins("JOIN clients AS c ON c.id = ci.client_id").
		Where("c.admin_id = ?", ownerId).
		Order("ci.inbound_id ASC, c.email ASC").
		Scan(&pairs).Error
	if err != nil {
		return nil, err
	}
	byInbound := make(map[int][]string, len(pairs))
	for _, p := range pairs {
		byInbound[p.InboundId] = append(byInbound[p.InboundId], p.Email)
	}

	links := []string{}
	for _, inbound := range inbounds {
		for _, email := range byInbound[inbound.Id] {
			links = append(links, registeredSubLinkProvider.LinksForClient(host, inbound, email)...)
		}
	}
	return links, nil
}
