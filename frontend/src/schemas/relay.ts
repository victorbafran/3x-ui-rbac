import { z } from 'zod';

// A relay is a plain server in another country that a cisco inbound's traffic
// can leave through. The panel installs WireGuard on it over SSH and nothing
// else — no panel, no xray, no account — so what comes back here is the row
// plus whatever the tunnel is currently doing.

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
  // A front's own: what its client last said when it is not running, and the
  // customer ports it carries right now.
  tunnelDetail: z.string().default(''),
  forwardedPorts: z
    .array(z.number())
    .nullish()
    .transform((v) => v ?? []),
});
export type Relay = z.infer<typeof RelaySchema>;

// Backhaul's transports, in the order worth trying. tcp suits almost every
// network; the others are only worth reaching for when a network refuses it.
export const TUNNEL_TRANSPORTS = [
  'tcp',
  'tcpmux',
  'ws',
  'wsmux',
  'wss',
  'wssmux',
  'udp',
  'udpovertcp',
] as const;
export const DEFAULT_TUNNEL_PORT = 3080;
export const RelayListSchema = z.object({ obj: z.array(RelaySchema).nullable().default([]) });

export const RelayOptionSchema = z.object({
  interface: z.string(),
  name: z.string(),
  label: z.string().default(''),
  address: z.string().default(''),
  country: z.string().default(''),
  countryCode: z.string().default(''),
  ready: z.boolean().default(false),
});
export type RelayOption = z.infer<typeof RelayOptionSchema>;
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
}
