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

/** A tunnel name the WireGuard apps accept, numbered when there are several. */
export function wireguardTunnelName(base: string, number?: number): string {
  const suffix = number ? `-${number}` : '';
  const clean = base
    .replace(/[^a-zA-Z0-9_=+.-]+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^[-.]+|-+$/g, '');
  const head = clean.slice(0, WIREGUARD_NAME_MAX - suffix.length).replace(/[-.]+$/, '');
  return (head || 'wg') + suffix;
}

export function formatTunnelConfigMeta(
  inbound: { id?: number; tag?: string; remark?: string },
  email?: string,
  totalCount = 1,
  endpoint = '',
  index = 0,
): {
  label?: string;
  fileName: string;
  qrRemark: string;
} {
  const inboundName =
    formatInboundLabel(inbound.tag, inbound.remark) ||
    (inbound.id != null ? `inbound-${inbound.id}` : '');
  const name = [inboundName, endpoint].filter(Boolean).join(' - ');
  const label = totalCount > 1 ? name : undefined;
  const number = totalCount > 1 ? index + 1 : undefined;
  const fileName = `${wireguardTunnelName(email || 'client', number)}.conf`;
  const qrRemark = totalCount > 1 && name ? [name, email].filter(Boolean).join(' - ') : email || '';

  return { label, fileName, qrRemark };
}
