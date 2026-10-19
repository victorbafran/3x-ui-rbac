package authz

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Permission keys are dotted "group.action". The group is what the UI shows
// as a section; sections.* also decides which sidebar entries an admin sees.
const (
	SecInbounds  = "sections.inbounds"
	SecClients   = "sections.clients"
	SecGroups    = "sections.groups"
	SecHosts     = "sections.hosts"
	SecNodes     = "sections.nodes"
	SecOutbounds = "sections.outbounds"
	SecRouting   = "sections.routing"
	SecXray      = "sections.xray"
	SecSettings  = "sections.settings"
	SecAdmins    = "sections.admins"
	// Relays are the servers a country's traffic leaves through. Enrolling
	// one hands the panel root on another machine, so like sections.admins
	// this is in no role's defaults: only a super_admin has it, which keeps
	// the sidebar entry and the super-only routes agreeing with each other.
	SecRelays = "sections.relays"

	ClientCreate          = "clients.create"
	ClientEdit            = "clients.edit"
	ClientDelete          = "clients.delete"
	ClientResetTraffic    = "clients.resetTraffic"
	ClientUnlimitedData   = "clients.unlimitedData"
	ClientUnlimitedExpiry = "clients.unlimitedExpiry"
	ClientViewLinks       = "clients.viewLinks"

	InboundCreate       = "inbounds.create"
	InboundEdit         = "inbounds.edit"
	InboundDelete       = "inbounds.delete"
	InboundResetTraffic = "inbounds.resetTraffic"
)

// AllPermissions is the closed set of keys. An override naming anything else
// is dropped, and Has() on an unknown key is false.
var AllPermissions = []string{
	SecInbounds, SecClients, SecGroups, SecHosts, SecNodes, SecOutbounds, SecRouting, SecXray, SecSettings, SecAdmins, SecRelays,
	ClientCreate, ClientEdit, ClientDelete, ClientResetTraffic, ClientUnlimitedData, ClientUnlimitedExpiry, ClientViewLinks,
	InboundCreate, InboundEdit, InboundDelete, InboundResetTraffic,
}

var knownPermission = func() map[string]bool {
	m := make(map[string]bool, len(AllPermissions))
	for _, k := range AllPermissions {
		m[k] = true
	}
	return m
}()

// Permissions is an admin's effective switch set.
type Permissions map[string]bool

// Has is false for anything not switched on, unknown keys included.
func (p Permissions) Has(key string) bool { return p[key] }

func grant(keys ...string) Permissions {
	p := Permissions{}
	for _, k := range keys {
		p[k] = true
	}
	return p
}

// RoleDefaults is the template a role pre-fills. super_admin is listed for the
// UI's sake only; the middleware never consults permissions for that role.
func RoleDefaults(role model.Role) Permissions {
	switch role {
	case model.RoleSuperAdmin:
		return grant(AllPermissions...)
	case model.RoleManager:
		return grant(
			SecInbounds, SecClients, SecGroups, SecHosts, SecNodes, SecOutbounds, SecRouting, SecXray,
			ClientCreate, ClientEdit, ClientDelete, ClientResetTraffic, ClientUnlimitedData, ClientUnlimitedExpiry, ClientViewLinks,
			InboundCreate, InboundEdit, InboundDelete, InboundResetTraffic,
		)
	case model.RoleReseller:
		// Works inside assigned inbounds only; cannot create inbounds or hand
		// out unlimited plans unless the super_admin switches those on.
		return grant(
			SecInbounds, SecClients,
			ClientCreate, ClientEdit, ClientResetTraffic, ClientViewLinks,
		)
	case model.RoleReadOnly:
		return grant(SecInbounds, SecClients, ClientViewLinks)
	default:
		return Permissions{}
	}
}

// ParseOverrides reads the users.permissions column: a JSON object of key →
// bool. Empty means "no overrides". Unknown keys are dropped, not kept.
func ParseOverrides(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]bool{}, nil
	}
	var in map[string]bool
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		if knownPermission[k] {
			out[k] = v
		}
	}
	return out, nil
}

// EncodeOverrides is the inverse of ParseOverrides, with keys sorted so the
// stored column is stable and diffs stay readable.
func EncodeOverrides(overrides map[string]bool) string {
	if len(overrides) == 0 {
		return ""
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		if knownPermission[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	ordered := make(map[string]bool, len(keys))
	for _, k := range keys {
		ordered[k] = overrides[k]
	}
	b, _ := json.Marshal(ordered)
	return string(b)
}

// Effective is the role template with the admin's own overrides laid on top.
// A malformed column falls back to the bare template rather than to nothing,
// so a corrupt row demotes an admin to their role, never locks them out.
func Effective(u *model.User) Permissions {
	p := RoleDefaults(u.Role)
	overrides, err := ParseOverrides(u.Permissions)
	if err != nil {
		return p
	}
	for k, v := range overrides {
		p[k] = v
	}
	return p
}
