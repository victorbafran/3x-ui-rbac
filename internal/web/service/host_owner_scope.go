package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// GetHostsForOwner is GetHosts narrowed to the hosts hanging off inbounds this
// admin owns. Host rows carry domains and SNI names, so an unscoped list hands
// a reseller another admin's front-end setup.
func (s *HostService) GetHostsForOwner(ownerId int) ([]*entity.HostGroup, error) {
	if ownerId <= 0 {
		return s.GetHosts()
	}
	var hosts []*model.Host
	err := database.GetDB().
		Joins("JOIN inbounds ON inbounds.id = hosts.inbound_id").
		Where(database.OwnedInboundCond("inbounds"), ownerId, ownerId).
		Order("hosts.inbound_id asc, hosts.sort_order asc, hosts.id asc").
		Find(&hosts).Error
	if err != nil {
		return nil, err
	}
	return groupHosts(hosts), nil
}
