import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { RelayListSchema, RelayOptionListSchema } from '@/schemas/relay';
import type { Relay, RelayOption } from '@/schemas/relay';
import { keys } from '@/api/queryKeys';

export type { Relay, RelayOption };

async function fetchRelays(): Promise<Relay[]> {
  const msg = await HttpUtil.get('/panel/api/relays/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch relays');
  const validated = parseMsg(msg, RelayListSchema, 'relays/list');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

// Enrolling a relay takes minutes: the panel has to reach the server, install
// WireGuard and prove the tunnel. The row appears at once as pending, so the
// list refreshes on its own while that happens rather than leaving the
// operator pressing reload to find out whether it worked.
export function useRelaysQuery() {
  const query = useQuery({
    queryKey: keys.relays.list(),
    queryFn: fetchRelays,
    refetchInterval: (q) => {
      const rows = q.state.data;
      if (!Array.isArray(rows)) return false;
      return rows.some((r) => r.status === 'pending' || r.status === 'working') ? 4000 : 30000;
    },
  });
  const relays = useMemo(() => query.data ?? [], [query.data]);
  return { ...query, relays };
}

async function fetchRelayOptions(): Promise<RelayOption[]> {
  const msg = await HttpUtil.get('/panel/api/relays/options', undefined, { silent: true });
  if (!msg?.success) return [];
  const validated = parseMsg(msg, RelayOptionListSchema, 'relays/options');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

// Used by the cisco inbound form to offer countries by name instead of asking
// an operator to remember an interface name. A reseller editing an inbound has
// no access to the relay routes, so a failure here is silent and the field
// falls back to free text.
export function useRelayOptions() {
  const query = useQuery({
    queryKey: keys.relays.options(),
    queryFn: fetchRelayOptions,
    staleTime: 60_000,
    retry: false,
  });
  return { ...query, options: query.data ?? [] };
}
