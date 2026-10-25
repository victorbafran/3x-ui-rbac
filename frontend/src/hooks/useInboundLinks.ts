import { useQuery } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import { useMeQuery } from '@/api/queries/useMeQuery';
import type { InboundClientLinks } from '@/generated/types';
import { InboundClientLinksSchema } from '@/generated/zod';

export type { InboundClientLinks };

const InboundClientLinksListSchema = z.array(InboundClientLinksSchema);
const NO_ENTRIES: InboundClientLinks[] = [];

// One entry per client of the inbound the caller may see, in settings order.
export async function fetchInboundClientLinks(inboundId: number): Promise<InboundClientLinks[]> {
  const msg = await HttpUtil.get(`/panel/api/inbounds/${inboundId}/links`);
  if (!msg?.success) return NO_ENTRIES;
  const parsed = InboundClientLinksListSchema.safeParse(msg.obj);
  if (parsed.success) return parsed.data;
  console.warn('[zod] inbounds/links response failed validation', parsed.error.issues);
  return NO_ENTRIES;
}

export function useInboundLinks(inboundId: number | undefined, open: boolean) {
  const { can } = useMeQuery();
  const id = inboundId && inboundId > 0 ? inboundId : 0;
  const query = useQuery({
    queryKey: keys.inbounds.links(id),
    queryFn: () => fetchInboundClientLinks(id),
    enabled: open && id > 0 && can('clients.viewLinks'),
    // Host and entry-server edits invalidate nothing here: each open asks anew.
    staleTime: 0,
  });
  return query.data ?? NO_ENTRIES;
}

export interface InboundLinksExport {
  content: string;
  configs?: string;
  links?: string;
}

// A tunnel inbound exports its .conf files with the share links on a second tab;
// every other protocol exports the links alone.
export function inboundLinksExport(
  protocol: string,
  entries: InboundClientLinks[],
): InboundLinksExport {
  const links = entries.flatMap((e) => e.links).join('\r\n');
  if (protocol !== 'wireguard' && protocol !== 'amneziawg') return { content: links };
  const configs = entries.flatMap((e) => e.configs.map((c) => c.text)).join('\r\n');
  return { content: configs, configs, links };
}
