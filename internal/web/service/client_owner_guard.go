package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The gate in authz checks ownership of a target named in the URL. Bulk
// client writes name their targets in the body, so these helpers let the
// handler ask the same question before it acts. ownerId 0 owns everything.

// ForeignInbounds returns the ids the admin may not touch, including ids that
// do not exist: an unknown id is refused, not ignored.
func (s *ClientService) ForeignInbounds(ownerId int, inboundIds []int) ([]int, error) {
	if ownerId <= 0 || len(inboundIds) == 0 {
		return nil, nil
	}
	var owned []int
	err := database.GetDB().Model(&model.Inbound{}).
		Where("id IN ?", inboundIds).
		Scopes(database.OwnedInbounds(ownerId)).
		Pluck("id", &owned).Error
	if err != nil {
		return nil, err
	}
	ok := make(map[int]bool, len(owned))
	for _, id := range owned {
		ok[id] = true
	}
	var foreign []int
	for _, id := range inboundIds {
		if !ok[id] {
			foreign = append(foreign, id)
		}
	}
	return foreign, nil
}

// OwnedEmailSet lists the client emails whose every inbound the admin owns.
// It is the filter the collection reads apply before answering a reseller.
func (s *ClientService) OwnedEmailSet(ownerId int) (map[string]bool, error) {
	var emails []string
	err := database.GetDB().Table("clients AS c").
		Where(ownedClientsCond, ownerId).
		Pluck("c.email", &emails).Error
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(emails))
	for _, e := range emails {
		set[e] = true
	}
	return set, nil
}

// ForeignEmails returns the emails the admin may not touch: clients on any
// inbound they do not own, and emails that match no client at all.
func (s *ClientService) ForeignEmails(ownerId int, emails []string) ([]string, error) {
	if ownerId <= 0 || len(emails) == 0 {
		return nil, nil
	}
	owned, err := s.OwnedEmailSet(ownerId)
	if err != nil {
		return nil, err
	}
	var foreign []string
	for _, e := range emails {
		if !owned[e] {
			foreign = append(foreign, e)
		}
	}
	return foreign, nil
}

// ForeignSubId reports whether any client under subId sits outside the
// admin's inbounds. An unknown subId is foreign.
func (s *ClientService) ForeignSubId(ownerId int, subId string) (bool, error) {
	if ownerId <= 0 {
		return false, nil
	}
	var emails []string
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("sub_id = ?", subId).Pluck("email", &emails).Error; err != nil {
		return true, err
	}
	if len(emails) == 0 {
		return true, nil
	}
	foreign, err := s.ForeignEmails(ownerId, emails)
	return len(foreign) > 0, err
}

// ForeignClientId is ForeignSubId for a single client row id.
func (s *ClientService) ForeignClientId(ownerId int, clientId int) (bool, error) {
	if ownerId <= 0 {
		return false, nil
	}
	var email string
	err := database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", clientId).Pluck("email", &email).Error
	if err != nil || email == "" {
		return true, err
	}
	foreign, err := s.ForeignEmails(ownerId, []string{email})
	return len(foreign) > 0, err
}

// OwnedInboundTagSet lists the tags of the admin's inbounds, for filtering the
// active-inbound trees the overview polls.
func (s *ClientService) OwnedInboundTagSet(ownerId int) (map[string]bool, error) {
	var tags []string
	err := database.GetDB().Model(&model.Inbound{}).
		Scopes(database.OwnedInbounds(ownerId)).
		Pluck("tag", &tags).Error
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(tags))
	for _, tag := range tags {
		set[tag] = true
	}
	return set, nil
}

// OwnedInboundsByAdmin is OwnedInboundTagSet for many admins in one query: per
// admin, the tag of each inbound they own, by id.
func (s *ClientService) OwnedInboundsByAdmin(ownerIds []int) (map[int]map[int]string, error) {
	byAdmin := make(map[int]map[int]string, len(ownerIds))
	if len(ownerIds) == 0 {
		return byAdmin, nil
	}
	var rows []struct {
		AdminId int
		Id      int
		Tag     string
	}
	err := database.GetDB().Table("users AS u").
		Select("u.id AS admin_id, inbounds.id, inbounds.tag").
		Joins("JOIN inbounds ON "+database.OwnedInboundByCond("inbounds", "u.id")).
		Where("u.id IN ?", ownerIds).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if byAdmin[r.AdminId] == nil {
			byAdmin[r.AdminId] = make(map[int]string)
		}
		byAdmin[r.AdminId][r.Id] = r.Tag
	}
	return byAdmin, nil
}

// OwnedInboundIds lists the inbounds the admin created or was assigned,
// ascending. It is what stands in for a choice the admin cannot make.
func (s *ClientService) OwnedInboundIds(ownerId int) ([]int, error) {
	ids := []int{}
	err := database.GetDB().Model(&model.Inbound{}).
		Scopes(database.OwnedInbounds(ownerId)).
		Order("id ASC").
		Pluck("id", &ids).Error
	return ids, err
}
