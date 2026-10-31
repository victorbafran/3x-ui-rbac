/**
 * Display label for an inbound: the remark when one is set, otherwise the
 * inbound tag. Falls back to an empty string when neither is present.
 */
export function formatInboundLabel(tag?: string, remark?: string): string {
  const remarkText = (remark || '').trim();
  if (remarkText) return remarkText;
  return (tag || '').trim();
}

// The WireGuard apps name the tunnel after the imported file and call anything
// but [a-zA-Z0-9_=+.-], up to 15 characters on Android, an invalid name.
const WIREGUARD_NAME_MAX = 15;

function wireguardTunnelName(base: string, tag: string): string {
  const suffix = tag ? `-${tag}` : '';
  const clean = base
    .replace(/[^a-zA-Z0-9_=+.-]+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^[-.]+|-+$/g, '');
  const head = clean.slice(0, WIREGUARD_NAME_MAX - suffix.length).replace(/[-.]+$/, '');
  return (head || 'wg') + suffix;
}

const FLAG = /[\u{1F1E6}-\u{1F1FF}]{2}/u;
const REGIONAL_INDICATOR_TO_LETTER = 0x1f1e6 - 0x41;

/** The country of the first flag emoji in a name, "DE" for 🇩🇪; "" without one. */
export function flagCountryCode(name: string): string {
  const flag = FLAG.exec(name)?.[0] ?? '';
  return String.fromCharCode(
    ...Array.from(flag, (indicator) => indicator.codePointAt(0)! - REGIONAL_INDICATOR_TO_LETTER),
  );
}

// Distinct tags with no "-" in them keep the names distinct however each head is cut.
function wireguardTunnelTags(configNames: string[]): string[] {
  const countries = configNames.map(flagCountryCode);
  const total = new Map<string, number>();
  for (const country of countries) total.set(country, (total.get(country) ?? 0) + 1);
  const seen = new Map<string, number>();
  return countries.map((country, i) => {
    if (!country) return configNames.length > 1 ? String(i + 1) : '';
    if (total.get(country) === 1) return country;
    const n = (seen.get(country) ?? 0) + 1;
    seen.set(country, n);
    return `${country}${n}`;
  });
}

/** Each config's .conf name: the client's name, else the config's own, cut ahead of its flag's
 *  country, numbered when that repeats; a config with no flag is numbered by its place.
 *  `client` is one name for every config, or one per config ("" where it has none). */
export function wireguardFileNames(
  configNames: string[],
  client: string | readonly string[] = '',
): string[] {
  const tags = wireguardTunnelTags(configNames);
  return configNames.map((name, i) => {
    const base = (typeof client === 'string' ? client : client[i]) || name;
    return `${wireguardTunnelName(base, tags[i])}.conf`;
  });
}

// A name runs on through letters, digits and these, so "bob" is not in "bobby" or "bob.smith".
const NAME_CHAR = /[\p{L}\p{N}_.@]/u;

/** The one name of `names` a remark carries whole; "" for none, or for two. */
export function clientNameInRemark(remark: string, names: readonly string[]): string {
  const found = names.filter((name) => {
    if (!name) return false;
    for (let at = remark.indexOf(name); at >= 0; at = remark.indexOf(name, at + 1)) {
      const before = remark.slice(0, at).at(-1) ?? '';
      const after = remark.slice(at + name.length)[0] ?? '';
      if (!NAME_CHAR.test(before) && !NAME_CHAR.test(after)) return true;
    }
    return false;
  });
  return new Set(found).size === 1 ? found[0] : '';
}

export function formatTunnelConfigMeta(
  inbound: { id?: number; tag?: string; remark?: string },
  email?: string,
  totalCount = 1,
  endpoint = '',
): {
  label?: string;
  qrRemark: string;
} {
  const inboundName =
    formatInboundLabel(inbound.tag, inbound.remark) ||
    (inbound.id != null ? `inbound-${inbound.id}` : '');
  const name = [inboundName, endpoint].filter(Boolean).join(' - ');
  const label = totalCount > 1 ? name : undefined;
  const qrRemark = totalCount > 1 && name ? [name, email].filter(Boolean).join(' - ') : email || '';

  return { label, qrRemark };
}
