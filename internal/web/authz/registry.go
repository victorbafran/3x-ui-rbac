package authz

type prefixRule struct {
	Prefix string
	Rule   Rule
}

// Anything under these prefixes is super_admin only, including routes added
// upstream later: a new global endpoint is closed, not open, until listed below.
var prefixRules = []prefixRule{
	{"/panel/api/server/", Rule{Roles: Admins}},
	{"/panel/api/xray/", Rule{Roles: Admins}},
	{"/panel/api/nodes", Rule{Roles: Admins}},
	{"/panel/api/setting/", Rule{Roles: Admins}},
	{"/panel/api/sub-balancers", Rule{Roles: Admins}},
	{"/panel/api/hosts/", Rule{Roles: Managers}},
	{"/panel/api/inbounds/", Rule{Roles: Admins}},
	{"/panel/api/clients/", Rule{Roles: Admins}},
}

// Exact entries open a route wider than its prefix default, and name the
// ownership check that replaces the blanket super_admin gate.
var exactRules = map[string]Rule{
	"GET /panel/api/openapi.json": {Roles: Readers},

	// The panel shell cannot render without these; non-admins get the
	// allowlisted view from AllSettingView.ForReseller.
	"POST /panel/api/setting/all":             {Roles: Readers},
	"POST /panel/api/setting/defaultSettings": {Roles: Readers},

	// The overview is the landing page for every role and polls these. They
	// are host metrics and the server's own address, which a reseller already
	// knows -- their clients connect to it. Everything else under /server/
	// stays super_admin by the prefix rule.
	"GET /panel/api/server/status":                  {Roles: Readers},
	"GET /panel/api/server/history/:metric/:bucket": {Roles: Readers},
	"GET /panel/api/server/cpuHistory/:bucket":      {Roles: Readers},
	"GET /panel/api/server/getPanelUpdateInfo":      {Roles: Readers},
	"POST /panel/api/backuptotgbot":                 {Roles: Admins},

	// Inbounds: lists already filter by owner inside the service.
	"GET /panel/api/inbounds/list":          {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/list/slim":     {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/options":       {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/allLinks":      {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/inbounds/get/:id":       {Roles: Readers, Scope: ScopeInbound, Param: "id"},
	"GET /panel/api/inbounds/:id/fallbacks": {Roles: Readers, Scope: ScopeInbound, Param: "id"},

	"POST /panel/api/inbounds/add":                {Roles: Operators},
	"POST /panel/api/inbounds/import":             {Roles: Operators},
	"POST /panel/api/inbounds/update/:id":         {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/del/:id":            {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/setEnable/:id":      {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/resetTraffic":   {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/subSortIndex":   {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/delAllClients":  {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/:id/fallbacks":      {Roles: Operators, Scope: ScopeInbound, Param: "id"},
	"POST /panel/api/inbounds/bulkDel":            {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/inbounds/resetAllTraffics":   {Roles: Managers},
	"POST /panel/api/inbounds/pushClientTraffics": {Roles: Admins},

	// Clients addressed by email: ownership runs through their inbounds.
	"GET /panel/api/clients/get/:email":            {Roles: Readers, Scope: ScopeClient, Param: "email"},
	"GET /panel/api/clients/links/:email":          {Roles: Readers, Scope: ScopeClient, Param: "email"},
	"GET /panel/api/clients/traffic/:email":        {Roles: Readers, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/update/:email":        {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/del/:email":           {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/resetTraffic/:email":  {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/updateTraffic/:email": {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/clearIps/:email":      {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/ips/:email":           {Roles: Readers, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/attach":        {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/detach":        {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/:email/externalLinks": {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"POST /panel/api/clients/hwids/:email":         {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"DELETE /panel/api/clients/hwids/:email":       {Roles: Operators, Scope: ScopeClient, Param: "email"},
	"DELETE /panel/api/clients/hwids/:email/:id":   {Roles: Operators, Scope: ScopeClient, Param: "email"},

	// Client collections: the service must filter these by owner.
	"GET /panel/api/clients/list":              {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/list/paged":        {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/export":            {Roles: Managers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/add":              {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkCreate":       {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDel":          {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkAdjust":       {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkEnable":       {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDisable":      {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkResetTraffic": {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkAttach":       {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/bulkDetach":       {Roles: Operators, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/activeInbounds":   {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/onlines":          {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/onlinesByGuid":    {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/lastOnline":       {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/clientIpsByGuid":  {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/renewalPreview":   {Roles: Operators, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/subLinks/:subId":   {Roles: Readers, Scope: ScopeOwnedListing},
	"GET /panel/api/clients/get/tgId/:tgId":    {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/happLink/:id":     {Roles: Readers, Scope: ScopeOwnedListing},
	"POST /panel/api/clients/import":           {Roles: Managers},
	"POST /panel/api/clients/delDepleted":      {Roles: Managers},
	"POST /panel/api/clients/delOrphans":       {Roles: Admins},
	"POST /panel/api/clients/resetAllTraffics": {Roles: Managers},

	// Client groups are a manager-level naming layer, not per-reseller.
	"GET /panel/api/clients/groups":               {Roles: Readers},
	"GET /panel/api/clients/groups/:name/emails":  {Roles: Readers},
	"POST /panel/api/clients/groups/create":       {Roles: Managers},
	"POST /panel/api/clients/groups/rename":       {Roles: Managers},
	"POST /panel/api/clients/groups/delete":       {Roles: Managers},
	"POST /panel/api/clients/groups/bulkAdd":      {Roles: Managers},
	"POST /panel/api/clients/groups/bulkRemove":   {Roles: Managers},
	"POST /panel/api/clients/groups/resetTraffic": {Roles: Managers},

	// Both are read on the inbounds page by every role. The handlers narrow
	// what comes back: hosts to the caller's own inbounds, nodes to nothing.
	"GET /panel/api/hosts/list": {Roles: Readers},
	"GET /panel/api/nodes/list": {Roles: Readers},

	// Hosts hang off an inbound; reading one is scoped by that inbound.
	"GET /panel/api/hosts/byInbound/:inboundId": {Roles: Readers, Scope: ScopeInbound, Param: "inboundId"},
}
