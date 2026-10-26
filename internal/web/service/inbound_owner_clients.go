package service

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// GetInboundsForOwner is GetInbounds cut to the caller's own clients: an inbound
// shared with a reseller carries the other admins' client credentials.
func (s *InboundService) GetInboundsForOwner(ownerId int) ([]*model.Inbound, error) {
	inbounds, err := s.GetInbounds(ownerId)
	if err != nil {
		return nil, err
	}
	if err := keepOwnedClients(inbounds, ownerId); err != nil {
		return nil, err
	}
	return inbounds, nil
}

// GetInboundsSlimForOwner is GetInboundsSlim cut the same way; slim still names every email.
func (s *InboundService) GetInboundsSlimForOwner(ownerId int) ([]*model.Inbound, error) {
	inbounds, err := s.GetInboundsSlim(ownerId)
	if err != nil {
		return nil, err
	}
	if err := keepOwnedClients(inbounds, ownerId); err != nil {
		return nil, err
	}
	return inbounds, nil
}

// GetInboundDetailForOwner is GetInboundDetail cut the same way; the export and
// edit windows build from it.
func (s *InboundService) GetInboundDetailForOwner(id, ownerId int) (*model.Inbound, error) {
	inbound, err := s.GetInboundDetail(id)
	if err != nil {
		return nil, err
	}
	if err := keepOwnedClients([]*model.Inbound{inbound}, ownerId); err != nil {
		return nil, err
	}
	return inbound, nil
}

// OwnerView is a copy of a saved inbound cut the same way, for the save's reply;
// the row the caller holds keeps every admin's clients.
func (s *InboundService) OwnerView(inbound *model.Inbound, ownerId int) (*model.Inbound, error) {
	if inbound == nil || ownerId <= 0 {
		return inbound, nil
	}
	view := *inbound
	if err := keepOwnedClients([]*model.Inbound{&view}, ownerId); err != nil {
		return nil, err
	}
	return &view, nil
}

// ExistingClientEmails is the emails among clients that already name a client row,
// matched as AddInbound's duplicate check matches them; AddInbound attaches those.
func (s *InboundService) ExistingClientEmails(clients []model.Client) ([]string, error) {
	existing, err := s.emailSubIDsForClients(clients)
	if err != nil {
		return nil, err
	}
	var taken []string
	for _, client := range clients {
		if _, ok := existing[strings.ToLower(strings.TrimSpace(client.Email))]; ok && client.Email != "" {
			taken = append(taken, client.Email)
		}
	}
	return taken, nil
}

// keepOwnedClients narrows each inbound's settings.clients and ClientStats to
// ownerId's own clients; ownerId 0 is unscoped and leaves every row as read.
func keepOwnedClients(inbounds []*model.Inbound, ownerId int) error {
	if ownerId <= 0 || len(inbounds) == 0 {
		return nil
	}
	ids := make([]int, 0, len(inbounds))
	for _, ib := range inbounds {
		ids = append(ids, ib.Id)
	}
	byInbound, err := ownedEmailsByInbound(ownerId, ids)
	if err != nil {
		return err
	}
	for _, ib := range inbounds {
		owned := make(map[string]bool, len(byInbound[ib.Id]))
		for _, email := range byInbound[ib.Id] {
			owned[email] = true
		}
		ib.Settings = settingsWithOnlyClients(ib.Settings, owned)
		ib.ClientStats = statsWithOnlyClients(ib.ClientStats, owned)
	}
	return nil
}

// settingsWithOnlyClients drops the settings.clients entries whose email is not
// in keep. Nothing to drop returns settings byte-identical; unreadable ones fail closed.
func settingsWithOnlyClients(settings string, keep map[string]bool) string {
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(settings), &raw) != nil {
		return "{}"
	}
	list, has := raw["clients"]
	if !has {
		return settings
	}
	var clients []json.RawMessage
	listErr := json.Unmarshal(list, &clients)
	kept := make([]json.RawMessage, 0, len(clients))
	for _, entry := range clients {
		var c struct {
			Email string `json:"email"`
		}
		if json.Unmarshal(entry, &c) == nil && keep[c.Email] {
			kept = append(kept, entry)
		}
	}
	if listErr == nil && len(kept) == len(clients) {
		return settings
	}
	keptJSON, err := json.Marshal(kept)
	if err != nil {
		return "{}"
	}
	raw["clients"] = keptJSON
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

func statsWithOnlyClients(stats []xray.ClientTraffic, keep map[string]bool) []xray.ClientTraffic {
	kept := make([]xray.ClientTraffic, 0, len(stats))
	for _, st := range stats {
		if keep[st.Email] {
			kept = append(kept, st)
		}
	}
	if len(kept) == len(stats) {
		return stats
	}
	return kept
}
