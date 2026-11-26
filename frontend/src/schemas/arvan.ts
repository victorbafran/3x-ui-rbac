import { z } from 'zod';

// A front hosted on ArvanCloud keeps one active public IP and up to three tested
// reserves on the same server; a swap moves the DNS record and re-dials the tunnel.

// A server takes at most four IPs, so three reserves beside the active one.
export const ARVAN_MAX_RESERVES = 3;

export const ArvanKeyStatusSchema = z.object({
  configured: z.boolean().default(false),
  // False when the setting holds a key the panel can no longer decrypt.
  readable: z.boolean().default(false),
});
export type ArvanKeyStatus = z.infer<typeof ArvanKeyStatusSchema>;

export const ARVAN_IP_STATUSES = ['testing', 'healthy', 'dead'] as const;
export type ArvanIPStatus = (typeof ARVAN_IP_STATUSES)[number];

export const ArvanReserveSchema = z.object({
  ip: z.string(),
  status: z.enum(ARVAN_IP_STATUSES).catch('testing'),
  lastCheck: z.number().default(0),
  lastError: z.string().default(''),
  iranOk: z.number().default(0),
  iranTotal: z.number().default(0),
  createdAt: z.number().default(0),
});
export type ArvanReserve = z.infer<typeof ArvanReserveSchema>;

export const ArvanCandidateSchema = z.object({
  domain: z.string(),
  name: z.string(),
  ip: z.string().default(''),
});
export type ArvanCandidate = z.infer<typeof ArvanCandidateSchema>;

export const ArvanFrontViewSchema = z.object({
  relayId: z.number(),
  relayName: z.string().default(''),
  keyConfigured: z.boolean().default(false),
  keyReadable: z.boolean().default(false),
  bound: z.boolean().default(false),
  zone: z.string().default(''),
  serverId: z.string().default(''),
  serverName: z.string().default(''),
  domain: z.string().default(''),
  recordName: z.string().default(''),
  recordId: z.string().default(''),
  recordIp: z.string().default(''),
  activeIp: z.string().default(''),
  activeHealthy: z.boolean().default(false),
  activeFails: z.number().default(0),
  reserveTarget: z.number().default(0),
  autoSwap: z.boolean().default(false),
  dailySwapAt: z.string().default(''),
  prepared: z.boolean().default(false),
  // The running action's label; "" when nothing is running on this front.
  busy: z.string().default(''),
  lastAction: z.string().default(''),
  lastError: z.string().default(''),
  lastSwap: z.number().default(0),
  lastCheck: z.number().default(0),
  // Go sends an empty slice as null, which default() would let through.
  reserves: z
    .array(ArvanReserveSchema)
    .nullish()
    .transform((v) => v ?? []),
  candidates: z
    .array(ArvanCandidateSchema)
    .nullish()
    .transform((v) => v ?? []),
  maxReserves: z.number().default(ARVAN_MAX_RESERVES),
  addsToday: z.number().default(0),
});
export type ArvanFrontView = z.infer<typeof ArvanFrontViewSchema>;

export const ArvanFrontFormSchema = z.object({
  domain: z.string(),
  recordName: z.string(),
  reserveTarget: z
    .number({ error: 'pages.relays.arvan.errors.reserveTarget' })
    .int('pages.relays.arvan.errors.reserveTarget')
    .min(0, 'pages.relays.arvan.errors.reserveTarget')
    .max(ARVAN_MAX_RESERVES, 'pages.relays.arvan.errors.reserveTarget'),
  autoSwap: z.boolean(),
  // "" turns the daily swap off; otherwise one or more HH:MM in the panel's time zone,
  // separated by commas (the Persian one too) or spaces. The server sorts them and caps the count.
  dailySwapAt: z
    .string()
    .trim()
    .regex(
      /^(?:|(?:[01]?\d|2[0-3]):[0-5]\d(?:\s*[,،\s]\s*(?:[01]?\d|2[0-3]):[0-5]\d)*)$/,
      'pages.relays.arvan.errors.dailySwapAt',
    ),
});
export type ArvanFrontFormValues = z.infer<typeof ArvanFrontFormSchema>;

// The DNS name a record serves; "@" is the domain's apex.
export function arvanRecordHost(name: string, domain: string): string {
  if (!domain) return name;
  return !name || name === '@' ? domain : `${name}.${domain}`;
}

// What a zone offers a new entry server: its plans with Arvan's own prices, and the Linux
// images that take an SSH key at creation.
const listOf = <T extends z.ZodTypeAny>(item: T) =>
  z
    .array(item)
    .nullish()
    .transform((v) => v ?? []);

export const ArvanCatalogSchema = z.object({
  zones: listOf(
    z.object({
      code: z.string(),
      city: z.string().default(''),
      dc: z.string().default(''),
      country: z.string().default(''),
    }),
  ),
  zone: z.string().default(''),
  sizes: listOf(
    z.object({
      id: z.string(),
      name: z.string().default(''),
      cpus: z.number().default(0),
      memoryGb: z.number().default(0),
      diskGb: z.number().default(0),
      pricePerHour: z.number().default(0),
      pricePerMonth: z.number().default(0),
      type: z.string().default(''),
    }),
  ),
  images: listOf(
    z.object({
      id: z.string(),
      distribution: z.string().default(''),
      version: z.string().default(''),
    }),
  ),
  serverCount: z.number().default(0),
  serverLimit: z.number().default(0),
  defaultImage: z.string().default(''),
});
export type ArvanCatalog = z.infer<typeof ArvanCatalogSchema>;

// A server on the account; relay names the relay of this panel at its address.
export const ArvanServerSchema = z.object({
  zone: z.string(),
  id: z.string(),
  name: z.string().default(''),
  status: z.string().default(''),
  ips: listOf(z.string()),
  relay: z.string().default(''),
});
export type ArvanServer = z.infer<typeof ArvanServerSchema>;

// The running or last make-and-add; step runs key → server → boot → ssh → add → bind → done.
export const ArvanCreateStateSchema = z.object({
  running: z.boolean().default(false),
  step: z.string().default(''),
  error: z.string().default(''),
  name: z.string().default(''),
  zone: z.string().default(''),
  serverId: z.string().default(''),
  ip: z.string().default(''),
  relayId: z.number().default(0),
  startedAt: z.number().default(0),
  finishedAt: z.number().default(0),
  // this run's steps in order; a server already there starts at "add", an exit stops at "setup"
  steps: listOf(z.string()),
});
export type ArvanCreateState = z.infer<typeof ArvanCreateStateSchema>;

// What a server from Arvan becomes here: an entry server carried by tool and transport, its IP
// behind record.domain when a domain is chosen, or an exit (only outside Iran).
export interface ArvanPurpose {
  role: 'front' | 'exit';
  tool: string;
  transport: string;
  domain: string;
  record: string;
}

export interface ArvanCreateRequest extends ArvanPurpose {
  zone: string;
  size: string;
  image: string;
  name: string;
}

export interface ArvanAdoptRequest extends ArvanPurpose {
  zone: string;
  serverId: string;
  name: string;
  password: string;
}
