package service

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// ownedHostRows narrows a hosts query to the rows on inbounds this admin owns:
// the one rule every scoped host read and write below goes through.
func ownedHostRows(tx *gorm.DB, ownerId int) *gorm.DB {
	return tx.Model(&model.Host{}).
		Joins("JOIN inbounds ON inbounds.id = hosts.inbound_id").
		Where(database.OwnedInboundCond("inbounds"), ownerId, ownerId)
}

// GetHostsForOwner is GetHosts over this admin's rows. Host rows carry domains
// and SNI names, so an unscoped list hands a reseller another admin's fronts.
func (s *HostService) GetHostsForOwner(ownerId int) ([]*entity.HostGroup, error) {
	if ownerId <= 0 {
		return s.GetHosts()
	}
	var hosts []*model.Host
	err := ownedHostRows(database.GetDB(), ownerId).
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
	err := ownedHostRows(database.GetDB(), ownerId).
		Order("hosts.sort_order asc, hosts.id asc").
		Find(&hosts).Error
	if err != nil {
		return nil, err
	}
	return hosts, nil
}

// GetHostGroupForOwner is GetHostGroup over the group's rows on this admin's
// inbounds; a group with none there answers exactly like a missing one.
func (s *HostService) GetHostGroupForOwner(groupId string, ownerId int) (*entity.HostGroup, error) {
	if ownerId <= 0 {
		return s.GetHostGroup(groupId)
	}
	var hosts []*model.Host
	err := ownedHostRows(database.GetDB(), ownerId).
		Where("hosts.group_id = ?", groupId).
		Order("hosts.sort_order asc, hosts.id asc").
		Find(&hosts).Error
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, common.NewError("host not found")
	}
	return groupHosts(hosts)[0], nil
}

// GetHostsByInboundForOwner is GetHostsByInbound over this admin's rows: the
// groups they have on that inbound, without the rows those keep elsewhere.
func (s *HostService) GetHostsByInboundForOwner(inboundId, ownerId int) ([]*entity.HostGroup, error) {
	if ownerId <= 0 {
		return s.GetHostsByInbound(inboundId)
	}
	db := database.GetDB()
	var groupIds []string
	if err := ownedHostRows(db, ownerId).Where("hosts.inbound_id = ?", inboundId).
		Distinct().Pluck("hosts.group_id", &groupIds).Error; err != nil {
		return nil, err
	}
	if len(groupIds) == 0 {
		return nil, nil
	}
	var hosts []*model.Host
	if err := ownedHostRows(db, ownerId).Where("hosts.group_id IN ?", groupIds).
		Order("hosts.sort_order asc, hosts.id asc").Find(&hosts).Error; err != nil {
		return nil, err
	}
	return groupHosts(hosts), nil
}

// GetAllTagsForOwner is GetAllTags over this admin's rows, so the tag picker
// does not spell out how other admins label their fronts.
func (s *HostService) GetAllTagsForOwner(ownerId int) ([]string, error) {
	if ownerId <= 0 {
		return s.GetAllTags()
	}
	var hosts []*model.Host
	if err := ownedHostRows(database.GetDB(), ownerId).Find(&hosts).Error; err != nil {
		return nil, err
	}
	return distinctHostTags(hosts), nil
}

// AddHostGroupForOwner is AddHostGroup for an admin whose rows may join only a
// new group or one they can already see; owner 0 is AddHostGroup itself.
func (s *HostService) AddHostGroupForOwner(req *entity.HostGroup, ownerId int) ([]*model.Host, error) {
	return addHostGroup(req, ownerId)
}

// claimableHostGroup refuses a group id with any row on another admin's inbound:
// joining it would put this admin's rows, and their values, into that admin's group.
func claimableHostGroup(tx *gorm.DB, groupId string, ownerId int) error {
	var total, owned int64
	if err := tx.Model(&model.Host{}).Where("group_id = ?", groupId).Count(&total).Error; err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	if err := ownedHostRows(tx, ownerId).Where("hosts.group_id = ?", groupId).Count(&owned).Error; err != nil {
		return err
	}
	if owned < total {
		return common.NewError("host group ID is already in use")
	}
	return nil
}

// splitHostGroups gives this admin's rows of a group that also spans other admins'
// inbounds a fresh id: unscoped views read a group off one row and save it to all.
func splitHostGroups(tx *gorm.DB, ownedRows map[string][]int) (map[string]string, error) {
	groupIds := make([]string, 0, len(ownedRows))
	for groupId := range ownedRows {
		groupIds = append(groupIds, groupId)
	}
	var totals []struct {
		GroupId string
		N       int
	}
	if err := tx.Model(&model.Host{}).Select("group_id, COUNT(*) AS n").
		Where("group_id IN ?", groupIds).Group("group_id").Scan(&totals).Error; err != nil {
		return nil, err
	}
	fresh := make(map[string]string)
	for _, total := range totals {
		if total.N > len(ownedRows[total.GroupId]) {
			fresh[total.GroupId] = random.NumLower(16)
		}
	}
	return fresh, nil
}

// splitChanges is a write of one column that also moves the rows to the fresh
// group splitHostGroups gave them, if any.
func splitChanges(fresh map[string]string, groupId, column string, value any) map[string]any {
	changes := map[string]any{column: value}
	if id, ok := fresh[groupId]; ok {
		changes["group_id"] = id
	}
	return changes
}

// UpdateHostGroupForOwner is UpdateHostGroup over the group's rows on this
// admin's inbounds; its rows on other admins' inbounds are left as they are.
func (s *HostService) UpdateHostGroupForOwner(groupId string, req *entity.HostGroup, ownerId int) ([]*model.Host, error) {
	return updateHostGroup(groupId, req, ownerId)
}

// ownedGroupRows is the ids of the named groups' rows on this admin's inbounds,
// per group. A group with no row there is unknown to them, and fails the call.
func ownedGroupRows(tx *gorm.DB, groupIds []string, ownerId int) (map[string][]int, error) {
	var rows []struct {
		Id      int
		GroupId string
	}
	if err := ownedHostRows(tx, ownerId).Where("hosts.group_id IN ?", groupIds).
		Select("hosts.id, hosts.group_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	byGroup := make(map[string][]int, len(groupIds))
	for _, r := range rows {
		byGroup[r.GroupId] = append(byGroup[r.GroupId], r.Id)
	}
	for _, id := range groupIds {
		if len(byGroup[id]) == 0 {
			return nil, common.NewError("host group not found")
		}
	}
	return byGroup, nil
}

func (s *HostService) DeleteHostGroupForOwner(groupId string, ownerId int) error {
	if ownerId <= 0 {
		return s.DeleteHostGroup(groupId)
	}
	return deleteOwnedHostGroups([]string{groupId}, ownerId)
}

func (s *HostService) DeleteHostsGroupForOwner(groupIds []string, ownerId int) error {
	if ownerId <= 0 {
		return s.DeleteHostsGroup(groupIds)
	}
	return deleteOwnedHostGroups(groupIds, ownerId)
}

// deleteOwnedHostGroups deletes the named groups' rows on this admin's inbounds,
// or nothing at all when one of the groups is not theirs.
func deleteOwnedHostGroups(groupIds []string, ownerId int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		byGroup, err := ownedGroupRows(tx, groupIds, ownerId)
		if err != nil {
			return err
		}
		var ids []int
		for _, groupRows := range byGroup {
			ids = append(ids, groupRows...)
		}
		_, err = database.DeleteHosts(tx, "id IN ?", ids)
		return err
	})
}

func (s *HostService) SetHostGroupEnableForOwner(groupId string, enable bool, ownerId int) error {
	if ownerId <= 0 {
		return s.SetHostGroupEnable(groupId, enable)
	}
	return enableOwnedHostGroups([]string{groupId}, enable, ownerId)
}

func (s *HostService) SetHostsGroupEnableForOwner(groupIds []string, enable bool, ownerId int) error {
	if ownerId <= 0 {
		return s.SetHostsGroupEnable(groupIds, enable)
	}
	return enableOwnedHostGroups(groupIds, enable, ownerId)
}

// enableOwnedHostGroups switches the named groups' rows on this admin's inbounds,
// or nothing at all when one of the groups is not theirs.
func enableOwnedHostGroups(groupIds []string, enable bool, ownerId int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		byGroup, err := ownedGroupRows(tx, groupIds, ownerId)
		if err != nil {
			return err
		}
		fresh, err := splitHostGroups(tx, byGroup)
		if err != nil {
			return err
		}
		for groupId, ids := range byGroup {
			if err := tx.Model(&model.Host{}).Where("id IN ?", ids).
				Updates(splitChanges(fresh, groupId, "is_disabled", !enable)).Error; err != nil {
				return err
			}
		}
		return nil
	})
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
		locked := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "hosts"}})
		if err := ownedHostRows(locked, ownerId).Order("hosts.id ASC").Find(&rows).Error; err != nil {
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
		fresh, err := splitHostGroups(tx, rowIds)
		if err != nil {
			return err
		}
		for i, groupId := range ordered {
			if err := tx.Model(&model.Host{}).Where("id IN ?", rowIds[groupId]).
				Updates(splitChanges(fresh, groupId, "sort_order", i)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
