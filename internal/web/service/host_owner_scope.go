package service

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
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

// GetRawHostsForOwner is GetRawHosts under the same rule: the client form's host
// picker would otherwise hand a reseller every admin's rows and group ids.
func (s *HostService) GetRawHostsForOwner(ownerId int) ([]*model.Host, error) {
	if ownerId <= 0 {
		return s.GetRawHosts()
	}
	var hosts []*model.Host
	err := database.GetDB().
		Joins("JOIN inbounds ON inbounds.id = hosts.inbound_id").
		Where(database.OwnedInboundCond("inbounds"), ownerId, ownerId).
		Order("hosts.sort_order asc, hosts.id asc").
		Find(&hosts).Error
	if err != nil {
		return nil, err
	}
	return hosts, nil
}

// ReorderHostGroupsForOwner is ReorderHostGroups over the rows on this admin's
// inbounds only; a group they cannot see is unknown, and no other row moves.
func (s *HostService) ReorderHostGroupsForOwner(groupIds []string, ownerId int) error {
	if ownerId <= 0 {
		return s.ReorderHostGroups(groupIds)
	}
	if len(groupIds) == 0 {
		return nil
	}
	seen, err := hostGroupIdSet(groupIds)
	if err != nil {
		return err
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var rows []*model.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "hosts"}}).
			Joins("JOIN inbounds ON inbounds.id = hosts.inbound_id").
			Where(database.OwnedInboundCond("inbounds"), ownerId, ownerId).
			Order("hosts.id ASC").Find(&rows).Error; err != nil {
			return err
		}
		rowIds := make(map[string][]int, len(rows))
		for _, h := range rows {
			rowIds[h.GroupId] = append(rowIds[h.GroupId], h.Id)
		}
		for _, id := range groupIds {
			if len(rowIds[id]) == 0 {
				return common.NewError("host group not found")
			}
		}
		ordered := append([]string(nil), groupIds...)
		for _, group := range groupHosts(rows) {
			if !seen[group.GroupId] && len(rowIds[group.GroupId]) > 0 {
				ordered = append(ordered, group.GroupId)
			}
		}
		for i, groupId := range ordered {
			if err := tx.Model(&model.Host{}).Where("id IN ?", rowIds[groupId]).Update("sort_order", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
