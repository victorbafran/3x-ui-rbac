package authz

type prefixRule struct {
	Prefix string
	Rule   Rule
}

// Anything under these prefixes is closed to everyone but super_admin,
// including routes added upstream later. An exact entry below opens a route.
var prefixRules = []prefixRule{
	{"/panel/api/server/", Rule{Perm: PermSuperOnly}},
	{"/panel/api/xray/", Rule{Perm: PermSuperOnly}},
	{"/panel/api/nodes", Rule{Perm: PermSuperOnly}},
	{"/panel/api/setting/", Rule{Perm: PermSuperOnly}},
	{"/panel/api/sub-balancers", Rule{Perm: PermSuperOnly}},
	{"/panel/api/admins", Rule{Perm: PermSuperOnly}},
	{"/panel/api/inbounds/", Rule{Perm: PermSuperOnly}},
	{"/panel/api/clients/", Rule{Perm: PermSuperOnly}},
	// Host management is a page of its own; an admin with that section runs it.
	{"/panel/api/hosts/", Rule{Perm: SecHosts}},
}

// Exact entries name the switch a route needs and how its target is owned.
// Perm "" is a deliberate choice, not an omission: the handler scopes the data.
var exactRules = map[string]Rule{
	"GET /panel/api/openapi.json":   {},
	"POST /panel/api/backuptotgbot": {Perm: PermSuperOnly},

	// Every admin may learn their own switches (the sidebar hides on them) and
	// read the catalog; everything else under /admins is closed by the prefix.
	"GET /panel/api/admins/me":          {},
	"GET /panel/api/admins/permissions": {},

	// The panel shell cannot render without these. Settings come back
	// allowlisted for non-admins; metrics are host load and the server's own
	// address, which a reseller's clients already connect to.
	"POST /panel/api/setting/all":                   {},
	"POST /panel/api/setting/defaultSettings":       {},
	"GET /panel/api/server/status":                  {},
	"GET /panel/api/server/history/:metric/:bucket": {},
	"GET /panel/api/server/cpuHistory/:bucket":      {},
	"GET /panel/api/server/getPanelUpdateInfo":      {},
	"GET /panel/api/hosts/list":                     {},
	"GET /panel/api/nodes/list":                     {},
	"GET /panel/api/hosts/byInbound/:inboundId":     {Perm: SecInbounds, Scope: ScopeInbound, Param: "inboundId"},

	// Inbounds. Lists filter by owner inside the service.
	"GET /panel/api/inbounds/list":          {Perm: SecInbounds, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/list/slim":     {Perm: SecInbounds, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/options":       {Perm: SecInbounds, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/allLinks":      {Perm: SecInbounds, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/get/:id":       {Perm: SecInbounds, Scope: ScopeInbound, Param: "id"},
	"GET /panel/api/inbounds/:id/fallbacks": {Perm: SecInbounds, Scope: ScopeInbound, Param: "id"},

	"POST /panel/api/inbounds/add":               {Perm: InboundCreate},
	"POST /panel/api/inbounds/import":            {Perm: InboundCreate},
	"POST /panel/api/inbounds/update/:id":        {Perm: InboundEdit, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/setEnable/:id":     {Perm: InboundEdit, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/subSortIndex":  {Perm: InboundEdit, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/fallbacks":     {Perm: InboundEdit, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/del/:id":           {Perm: InboundDelete, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/resetTraffic":  {Perm: InboundResetTraffic, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/delAllClients": {Perm: ClientDelete, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/bulkDel":           {Perm: InboundDelete, Scope: ScopeOwnedListing},
	// These two act on every inbound on the panel; the services do not scope.
	"POST /panel/api/inbounds/resetAllTraffics":   {Perm: PermSuperOnly},
	"POST /panel/api/inbounds/pushClientTraffics": {Perm: PermSuperOnly},

	// Clients addressed by email: ownership runs through their inbounds.
	"GET /panel/api/clients/get/:email":            {Perm: SecClients, Scope: ScopeClient, Param: "email"},
	"GET /panel/api/clients/links/:email":          {Perm: ClientViewLinks, Scope: ScopeClient, Param: "email"},
	"GET /panel/api/clients/traffic/:email":        {Perm: SecClients, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/ips/:email":           {Perm: SecClients, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/update/:email":        {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/updateTraffic/:email": {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/clearIps/:email":      {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/attach":        {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/detach":        {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/externalLinks": {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/hwids/:email":         {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"DELETE /panel/api/clients/hwids/:email":       {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"DELETE /panel/api/clients/hwids/:email/:id":   {Perm: ClientEdit, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/del/:email":           {Perm: ClientDelete, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/resetTraffic/:email":  {Perm: ClientResetTraffic, Scope: ScopeClient, Param: "email"},

	// Client collections: the service filters these by owner. The bulk
	// writers also carry target inbound ids in the body, which the handler
	// must still check against the caller's inbounds.
	"GET /panel/api/clients/list":              {Perm: SecClients, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/list/paged":        {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/activeInbounds":   {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/onlines":          {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/onlinesByGuid":    {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/lastOnline":       {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/clientIpsByGuid":  {Perm: SecClients, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/renewalPreview":   {Perm: SecClients, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/get/tgId/:tgId":    {Perm: SecClients, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/subLinks/:subId":   {Perm: ClientViewLinks, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/happLink/:id":     {Perm: ClientViewLinks, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/add":              {Perm: ClientCreate, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkCreate":       {Perm: ClientCreate, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/import":           {Perm: ClientCreate, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkAdjust":       {Perm: ClientEdit, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkEnable":       {Perm: ClientEdit, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDisable":      {Perm: ClientEdit, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkAttach":       {Perm: ClientEdit, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDetach":       {Perm: ClientEdit, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDel":          {Perm: ClientDelete, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkResetTraffic": {Perm: ClientResetTraffic, Scope: ScopeOwnedListing},
	// Panel-wide operations whose services do not scope: super_admin only.
	"GET /panel/api/clients/export":            {Perm: PermSuperOnly},
	"POST /panel/api/clients/delDepleted":      {Perm: PermSuperOnly},
	"POST /panel/api/clients/delOrphans":       {Perm: PermSuperOnly},
	"POST /panel/api/clients/resetAllTraffics": {Perm: PermSuperOnly},

	// Client groups are a panel-wide naming layer: one section switch for all.
	"GET /panel/api/clients/groups":               {Perm: SecGroups},
	"GET /panel/api/clients/groups/:name/emails":  {Perm: SecGroups},
	"POST /panel/api/clients/groups/create":       {Perm: SecGroups},
	"POST /panel/api/clients/groups/rename":       {Perm: SecGroups},
	"POST /panel/api/clients/groups/delete":       {Perm: SecGroups},
	"POST /panel/api/clients/groups/bulkAdd":      {Perm: SecGroups},
	"POST /panel/api/clients/groups/bulkRemove":   {Perm: SecGroups},
	"POST /panel/api/clients/groups/resetTraffic": {Perm: SecGroups},
}
