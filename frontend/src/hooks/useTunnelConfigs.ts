import { useQuery } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import type { TunnelConfig } from '@/generated/types';
import { TunnelConfigSchema } from '@/generated/zod';

export type { TunnelConfig };

const TunnelConfigListSchema = z.array(TunnelConfigSchema);
const NO_CONFIGS: TunnelConfig[] = [];

export function isClientRecordId(id: unknown): id is number {
  return typeof id === 'number' && Number.isSafeInteger(id) && id > 0;
}

async function fetchTunnelConfigs(clientId: number): Promise<TunnelConfig[]> {
  const msg = await HttpUtil.get(`/panel/api/clients/tunnelConfigs/${clientId}`);
  if (!msg?.success) return NO_CONFIGS;
  const parsed = TunnelConfigListSchema.safeParse(msg.obj);
  if (parsed.success) return parsed.data;
  console.warn('[zod] clients/tunnelConfigs response failed validation', parsed.error.issues);
  return NO_CONFIGS;
}

// The WireGuard, AmneziaWG and TUIC configs of one client record, rendered by the
// subscription so an entry server's IPv6 variants match what the client is served.
export function useTunnelConfigs(clientId: unknown, open: boolean) {
  const id = isClientRecordId(clientId) ? clientId : 0;
  const query = useQuery({
    queryKey: keys.clients.tunnelConfigs(id),
    queryFn: () => fetchTunnelConfigs(id),
    enabled: open && id > 0,
    // Inbound, Host and entry-server edits invalidate nothing here: each open asks anew.
    staleTime: 0,
  });
  return { configs: query.data ?? NO_CONFIGS, loading: query.isLoading };
}

// "IPv6"/"IPv4" for a config rendered for an entry server, "" for any other.
export function tunnelFamilyLabel(family: string): string {
  if (family === 'v6') return 'IPv6';
  if (family === 'v4') return 'IPv4';
  return '';
}
