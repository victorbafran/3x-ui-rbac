import { z } from 'zod';

// Psiphon runs on this panel's own server, one process per enabled country. An
// inbound offers a country through a host whose exit is psi-<CC>.

export const PSIPHON_WARP_MODES = ['off', 'sites', 'all'] as const;
export type PsiphonWarpMode = (typeof PSIPHON_WARP_MODES)[number];

// Measured on the live panel box: each country's process holds about this much.
export const PSIPHON_MB_PER_COUNTRY = 36;

// Go sends an empty slice as null, which default() would let through.
const codeList = z
  .array(z.string())
  .nullish()
  .transform((v) => v ?? []);

export const PsiphonCountrySchema = z.object({
  code: z.string(),
  name: z.string().default(''),
  flag: z.string().default(''),
  enabled: z.boolean().default(false),
  running: z.boolean().default(false),
  connected: z.boolean().default(false),
  // The region Psiphon says it connected through, which should equal code.
  region: z.string().default(''),
  exitIp: z.string().default(''),
  exitLoc: z.string().default(''),
  lastError: z.string().default(''),
  socksPort: z.number().default(0),
  // Bytes customers moved through the country since the last reset, saved every 30 s.
  trafficDown: z.number().default(0),
  trafficUp: z.number().default(0),
});
export type PsiphonCountry = z.infer<typeof PsiphonCountrySchema>;

export const PsiphonWarpSchema = z.object({
  mode: z.enum(PSIPHON_WARP_MODES).catch('off'),
  sites: z.string().default(''),
  registered: z.boolean().default(false),
});
export type PsiphonWarp = z.infer<typeof PsiphonWarpSchema>;

export const PsiphonStateSchema = z.object({
  // False on anything but x86_64 Linux: Psiphon publishes no other build.
  supported: z.boolean().default(false),
  installed: z.boolean().default(false),
  regions: codeList,
  regionsAt: z.number().default(0),
  countries: z
    .array(PsiphonCountrySchema)
    .nullish()
    .transform((v) => v ?? []),
  warp: PsiphonWarpSchema.nullish().transform((v) => v ?? PsiphonWarpSchema.parse({})),
});
export type PsiphonState = z.infer<typeof PsiphonStateSchema>;

// A local VLESS, Hysteria or WireGuard inbound and the countries its hosts carry.
export const PsiphonInboundSchema = z.object({
  inboundId: z.number(),
  remark: z.string().default(''),
  protocol: z.string().default(''),
  countries: codeList,
});
export type PsiphonInbound = z.infer<typeof PsiphonInboundSchema>;
export const PsiphonInboundListSchema = z.object({
  obj: z.array(PsiphonInboundSchema).nullable().default([]),
});

// The rule kinds the backend accepts for a WARP site (any case), or a bare domain;
// it still checks each value itself, so this only catches what is plainly wrong.
const SITE_RULE = /^(?:(?:domain|full|keyword|geosite):\S+|regexp:.+|[^\s:]+)$/i;

export const PsiphonWarpFormSchema = z.object({
  mode: z.enum(PSIPHON_WARP_MODES),
  sites: z.string().refine(
    (text) =>
      text
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean)
        .every((line) => SITE_RULE.test(line)),
    'pages.relays.psiphon.errors.siteRule',
  ),
});
export type PsiphonWarpFormValues = z.infer<typeof PsiphonWarpFormSchema>;

export type PsiphonPhase = 'off' | 'connecting' | 'connected' | 'error';

export function psiphonPhase(country: PsiphonCountry | undefined): PsiphonPhase {
  if (!country?.enabled) return 'off';
  if (country.connected) return 'connected';
  return country.lastError ? 'error' : 'connecting';
}

// A country connects within seconds of starting, so the card looks often while
// one is on its way and only now and then once all have settled.
export function psiphonRefetchMs(state: PsiphonState | undefined): number {
  return state?.countries.some((c) => psiphonPhase(c) === 'connecting') ? 5000 : 30000;
}

function flagOf(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return '';
  return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

function englishName(code: string): string {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' }).of(code) ?? code;
  } catch {
    return code;
  }
}

// Flag and English name, as customers see the country in their config's name;
// the backend's own wording wins once the country has a row.
export function psiphonCountryLabel(code: string, country?: PsiphonCountry): string {
  const flag = country?.flag || flagOf(code);
  const name = country?.name || englishName(code);
  return flag ? `${flag} ${name}` : name;
}
