package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// trafficPush is the traffic message's payload; its keys are the wire shape.
// A super_admin or manager panel receives it as is.
type trafficPush map[string]any

// ClientEmails names every client OwnedBy may keep: an email left out here is one
// the ownership lookup never asks about, and so one the reseller never sees.
func (p trafficPush) ClientEmails() []string {
	var emails []string
	if rows, ok := p["clientTraffics"].([]*xray.ClientTraffic); ok {
		emails = appendRowEmails(emails, rows)
	}
	if online, ok := p["onlineClients"].([]string); ok {
		emails = append(emails, online...)
	}
	if byGuid, ok := p["onlineByGuid"].(map[string][]string); ok {
		for _, online := range byGuid {
			emails = append(emails, online...)
		}
	}
	if lastOnline, ok := p["lastOnlineMap"].(map[string]int64); ok {
		for email := range lastOnline {
			emails = append(emails, email)
		}
	}
	return emails
}

// OwnedBy cuts every key down to the admin's own inbounds and clients, as their
// REST reads are cut. A key it does not know is left out rather than leaked.
func (p trafficPush) OwnedBy(owned websocket.Access) any {
	share := make(map[string]any, len(p))
	for key, value := range p {
		switch key {
		case "clientTrafficSource", "clientTrafficIntervalMs":
			share[key] = value
		case "traffics", "nodeTraffics":
			if rows, ok := value.([]*xray.Traffic); ok {
				share[key] = ownedInboundTraffic(rows, owned.InboundTags)
			}
		case "activeInbounds":
			if byGuid, ok := value.(map[string][]string); ok {
				share[key] = ownedNamesByGuid(byGuid, owned.InboundTags)
			}
		case "clientTraffics":
			if rows, ok := value.([]*xray.ClientTraffic); ok {
				share[key] = ownedRows(rows, owned.Emails)
			}
		case "onlineClients":
			if emails, ok := value.([]string); ok {
				share[key] = ownedNames(emails, owned.Emails)
			}
		case "onlineByGuid":
			if byGuid, ok := value.(map[string][]string); ok {
				share[key] = ownedNamesByGuid(byGuid, owned.Emails)
			}
		case "lastOnlineMap":
			if lastOnline, ok := value.(map[string]int64); ok {
				share[key] = ownedLastOnline(lastOnline, owned.Emails)
			}
		}
	}
	return share
}

// clientStatsPush is the client_stats message's payload: client rows and the
// inbound summaries.
type clientStatsPush map[string]any

func (p clientStatsPush) ClientEmails() []string {
	rows, _ := p["clients"].([]*xray.ClientTraffic)
	return appendRowEmails(nil, rows)
}

// OwnedBy keeps the admin's clients' rows and inbounds' summaries. Like the jobs,
// it sends nothing when the snapshot flag would be all that is left.
func (p clientStatsPush) OwnedBy(owned websocket.Access) any {
	share := make(map[string]any, len(p))
	for key, value := range p {
		switch key {
		case "snapshot":
			share[key] = value
		case "clients":
			if rows, ok := value.([]*xray.ClientTraffic); ok {
				if mine := ownedRows(rows, owned.Emails); len(mine) > 0 {
					share[key] = mine
				}
			}
		case "inbounds":
			if rows, ok := value.([]service.InboundTrafficSummary); ok {
				if mine := ownedSummaries(rows, owned.InboundIds); len(mine) > 0 {
					share[key] = mine
				}
			}
		}
	}
	_, hasRows := share["clients"]
	_, hasInbounds := share["inbounds"]
	if !hasRows && !hasInbounds {
		return nil
	}
	return share
}

func appendRowEmails(emails []string, rows []*xray.ClientTraffic) []string {
	for _, row := range rows {
		if row != nil {
			emails = append(emails, row.Email)
		}
	}
	return emails
}

func ownedRows(rows []*xray.ClientTraffic, owned map[string]bool) []*xray.ClientTraffic {
	mine := make([]*xray.ClientTraffic, 0, len(rows))
	for _, row := range rows {
		if row != nil && owned[row.Email] {
			mine = append(mine, row)
		}
	}
	return mine
}

// ownedInboundTraffic keeps the rows of the admin's inbounds, and no outbound's. A
// nil list stays nil: the page skips a null and keeps the speeds it shows.
func ownedInboundTraffic(rows []*xray.Traffic, tags map[string]bool) []*xray.Traffic {
	if rows == nil {
		return nil
	}
	mine := make([]*xray.Traffic, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.IsInbound && tags[row.Tag] {
			mine = append(mine, row)
		}
	}
	return mine
}

func ownedSummaries(rows []service.InboundTrafficSummary, ids map[int]bool) []service.InboundTrafficSummary {
	mine := make([]service.InboundTrafficSummary, 0, len(rows))
	for _, row := range rows {
		if ids[row.Id] {
			mine = append(mine, row)
		}
	}
	return mine
}

func ownedNames(names []string, owned map[string]bool) []string {
	mine := make([]string, 0, len(names))
	for _, name := range names {
		if owned[name] {
			mine = append(mine, name)
		}
	}
	return mine
}

// ownedNamesByGuid keeps every node's key: an empty list there says none of the
// admin's clients, or inbounds, is active on that node.
func ownedNamesByGuid(byGuid map[string][]string, owned map[string]bool) map[string][]string {
	mine := make(map[string][]string, len(byGuid))
	for guid, names := range byGuid {
		mine[guid] = ownedNames(names, owned)
	}
	return mine
}

func ownedLastOnline(lastOnline map[string]int64, owned map[string]bool) map[string]int64 {
	mine := make(map[string]int64)
	for email, at := range lastOnline {
		if owned[email] {
			mine[email] = at
		}
	}
	return mine
}
