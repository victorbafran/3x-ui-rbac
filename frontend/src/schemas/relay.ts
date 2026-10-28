import { z } from 'zod';

// A relay is a plain server in another country that a cisco inbound's traffic
// can leave through. The panel installs WireGuard on it over SSH and nothing
// else — no panel, no xray, no account — so what comes back here is the row
// plus whatever the tunnel is currently doing.

// What a front hands its customers: "" IPv4 as before, "v6", or "both", which
// gives each customer an IPv6 config first and the IPv4 one after it.
export const IP_FAMILIES = ['', 'v6', 'both'] as const;
export type IPFamily = (typeof IP_FAMILIES)[number];

// address6Error until the panel has dialled the address (address6NotChecked in
// internal/web/service/relay_ipv6.go): no verdict yet, neither reached nor failed.
export const ADDRESS6_NOT_CHECKED = 'not checked yet';

export const RelaySchema = z.object({
  id: z.number(),
  name: z.string(),
  label: z.string().default(''),
  address: z.string(),
  sshPort: z.number().default(22),
  sshUser: z.string().default('root'),
  tunnel: z.string().default('wireguard'),
  status: z.string().default('pending'),
  lastError: z.string().default(''),
  lastSeen: z.number().default(0),
  enabled: z.boolean().default(true),
  createdAt: z.number().default(0),
  wgPeerPublic: z.string().default(''),
  // Asked of the relay itself once it is reachable; the operator knows the
  // address they bought, not the city it ended up in.
  country: z.string().default(''),
  countryCode: z.string().default(''),
  city: z.string().default(''),

  interface: z.string().default(''),
  tunnelUp: z.boolean().default(false),
  healthy: z.boolean().default(false),
  handshakeAgo: z.number().default(0),
  received: z.number().default(0),
  sent: z.number().default(0),
  // Which cisco inbounds already offer this relay as a country. Deleting one
  // that is in use would leave that country in every customer's dropdown with
  // nothing behind it, so the panel says so rather than finding out later.
  // nullish, not default: default() only fills an *undefined* value, so a
  // null — which is what Go sends for an empty slice — went straight through
  // and the page died reading .length off it.
  inUseBy: z
    .array(z.string())
    .nullish()
    .transform((v) => v ?? []),

  // An exit carries customers out to its country. A front is the address
  // customers connect to, with the panel's own inbounds reaching it through
  // a tunnel. Rows written before fronts existed come back as exits.
  role: z.enum(['exit', 'front']).catch('exit'),
  tunnelPort: z.number().default(0),
  transport: z.string().default(''),
  // The name customers type to connect through a front. A TLS handshake from
  // inside Iran does not complete without a name, so it is the address an
  // inbound behind the front should hand out, not the bare IP.
  publicName: z.string().default(''),
  // A front's public IPv6, read off the server unless typed, and why the panel
  // could not read it (address6 still "") or reach it; "" once it could.
  address6: z.string().default(''),
  ipFamily: z.enum(IP_FAMILIES).catch(''),
  address6Error: z.string().default(''),
  // A name with only an AAAA record to address6, handed to IPv6 customers in
  // its place; publicName stays the IPv4 one, with only an A record.
  publicName6: z.string().default(''),
  // A front's own: what its client last said when it is not running, and the
  // customer ports it carries right now.
  tunnelDetail: z.string().default(''),
  forwardedPorts: z
    .array(z.number())
    .nullish()
    .transform((v) => v ?? []),
  // Forwarded ports whose service wants the customer's real address; only a
  // BackPack front carries them, on its second tunnel one port up.
  proxyPorts: z
    .array(z.number())
    .nullish()
    .transform((v) => v ?? []),
  // A BackPack front's customer bytes since it was added (out = what customers
  // downloaded), and the speed of the last sample in bytes/s; Backhaul counts none.
  trafficIn: z.number().default(0),
  trafficOut: z.number().default(0),
  rateIn: z.number().default(0),
  rateOut: z.number().default(0),
});
export type Relay = z.infer<typeof RelaySchema>;

// Backhaul's transports, in the order worth trying. tcp suits almost every
// network; the others are only worth reaching for when a network refuses it.
export type TunnelTool = 'backhaul' | 'backpack';
export const TUNNEL_TOOLS: readonly TunnelTool[] = ['backhaul', 'backpack'];

// What each tool runs, in the order worth trying. They must match the panel's
// own lists in internal/tunnel/tool.go; a transport the binary does not know
// is refused at startup and reads as a tunnel that never comes up.
export const TUNNEL_TRANSPORTS: Record<TunnelTool, readonly string[]> = {
  backhaul: ['tcp', 'tcpmux', 'ws', 'wsmux', 'wss', 'wssmux', 'udpovertcp'],
  backpack: ['stealth', 'tcp', 'tcpmux', 'ws', 'wsmux', 'wss', 'wssmux', 'kcp', 'quic'],
};
export const DEFAULT_TUNNEL_PORT = 3080;

// udp left the list because it cuts every TCP inbound off, but a front saved on
// it keeps it on offer, so an unrelated edit does not have to change it.
export function transportChoices(tool: TunnelTool, stored = ''): readonly string[] {
  const offered = TUNNEL_TRANSPORTS[tool];
  return stored && !offered.includes(stored) ? [...offered, stored] : offered;
}

// The transports that encrypt the tunnel; on the rest its token and anything
// not encrypted on its own cross the network readable. A Backhaul front's UDP
// tunnel, one port up, rides udp whatever is chosen here: it has a token of its
// own, but what crosses it is not encrypted.
const ENCRYPTED_TRANSPORTS: Record<TunnelTool, ReadonlySet<string>> = {
  backhaul: new Set(['wss', 'wssmux']),
  backpack: new Set(['stealth', 'wss', 'wssmux', 'kcp', 'quic']),
};
export function transportEncrypted(tool: string, transport: string): boolean {
  return ENCRYPTED_TRANSPORTS[toolOf(tool)].has(transport);
}

// Backhaul carries a front's UDP on a second tunnel one port up; BackPack
// carries it on the one. The operator has to open that port on the front's
// firewall too, so the page says which it is.
const TCP_CARRIED = new Set(['tcp', 'tcpmux', 'ws', 'wsmux', 'wss', 'wssmux']);
export function udpLegPort(tool: string, transport: string, tunnelPort: number): number {
  if (tool === 'backpack' || !TCP_CARRIED.has(transport) || !tunnelPort) return 0;
  return tunnelPort + 1;
}

export function toolOf(tunnel: string): TunnelTool {
  return tunnel === 'backpack' ? 'backpack' : 'backhaul';
}
export const RelayListSchema = z.object({ obj: z.array(RelaySchema).nullable().default([]) });

export const RelayOptionSchema = z.object({
  interface: z.string(),
  name: z.string(),
  label: z.string().default(''),
  address: z.string().default(''),
  country: z.string().default(''),
  countryCode: z.string().default(''),
  ready: z.boolean().default(false),
  // "psiphon" is a country Psiphon carries out of this panel: an Xray exit with
  // no interface, so cisco and OpenVPN, which route by interface, cannot use it.
  kind: z.string().default('relay'),
});
export type RelayOption = z.infer<typeof RelayOptionSchema>;
// An entry server as the inbound form offers it for the address customers'
// links carry.
export const FrontOptionSchema = z.object({
  id: z.number(),
  name: z.string(),
  label: z.string().default(''),
  address: z.string().default(''),
  publicName: z.string().default(''),
  ready: z.boolean().default(false),
});
export type FrontOption = z.infer<typeof FrontOptionSchema>;
export const FrontOptionListSchema = z.object({
  obj: z.array(FrontOptionSchema).nullable().default([]),
});

// What the share-address box suggests: each front's public name, or its
// address when it has none, labelled so the operator knows which server it is.
export function frontShareOptions(fronts: FrontOption[]): { value: string; label: string }[] {
  return fronts
    .map((f) => {
      const value = f.publicName || f.address;
      return { value, label: `${f.label || f.name} — ${value}` };
    })
    .filter((o) => o.value !== '');
}

export const RelayOptionListSchema = z.object({
  obj: z.array(RelayOptionSchema).nullable().default([]),
});

// What the probe found. An address is only worth selling as a country if the
// services customers ask for actually answer it, and that cannot be read off
// the address: the same request from two hosts gets two different answers.
export const ReachabilitySchema = z.object({
  Address: z.string().default(''),
  Country: z.string().default(''),
  City: z.string().default(''),
  Org: z.string().default(''),
  Services: z
    .record(z.string(), z.string())
    .nullish()
    .transform((v) => v ?? {}),
  Notes: z
    .array(z.string())
    .nullish()
    .transform((v) => v ?? []),
});
export type Reachability = z.infer<typeof ReachabilitySchema>;
export const ReachabilityMsgSchema = z.object({ obj: ReachabilitySchema.nullable() });

// Write-only: the password or key is used once to install the panel's own key
// and is never stored or sent back.
export interface RelayFormValues {
  name: string;
  label: string;
  address: string;
  sshPort: number;
  sshUser: string;
  password?: string;
  privateKey?: string;
  enabled?: boolean;
  // Fixed once the server is enrolled: the backend refuses to turn an exit
  // into a front or back, because each is installed differently.
  role?: 'exit' | 'front';
  tunnelPort?: number;
  transport?: string;
  tool?: TunnelTool;
  publicName?: string;
  // Blank lets the panel read the front's IPv6 off the server during setup.
  address6?: string;
  publicName6?: string;
  ipFamily?: IPFamily;
}
