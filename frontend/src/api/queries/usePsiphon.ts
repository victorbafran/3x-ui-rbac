import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import { parseMsg } from '@/utils/zodValidate';
import {
  PsiphonInboundListSchema,
  PsiphonStateSchema,
  psiphonRefetchMs,
  type PsiphonInbound,
  type PsiphonState,
  type PsiphonWarpFormValues,
} from '@/schemas/psiphon';

export type { PsiphonInbound, PsiphonState };

const BASE = '/panel/api/relays/psiphon';
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchState(): Promise<PsiphonState> {
  const msg = await HttpUtil.get(`${BASE}/state`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the Psiphon state');
  return PsiphonStateSchema.parse(msg.obj ?? {});
}

export function usePsiphonState() {
  return useQuery({
    queryKey: keys.relays.psiphon.state(),
    queryFn: fetchState,
    refetchInterval: (q) => psiphonRefetchMs(q.state.data),
  });
}

async function fetchInbounds(): Promise<PsiphonInbound[]> {
  const msg = await HttpUtil.get(`${BASE}/inbounds`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the inbounds');
  const validated = parseMsg(msg, PsiphonInboundListSchema, 'relays/psiphon/inbounds');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

// Read only once Psiphon is installed: before that no inbound can take a country.
export function usePsiphonInbounds(enabled: boolean) {
  return useQuery({
    queryKey: keys.relays.psiphon.inbounds(),
    queryFn: fetchInbounds,
    enabled,
  });
}

export function usePsiphonMutations() {
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: keys.relays.psiphon.root() });
  // Countries and an inbound's picks change the host form's exits and the
  // inbound's hosts, so both lists are read again, the card's with them.
  const refreshExits = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: keys.relays.root() }),
      queryClient.invalidateQueries({ queryKey: keys.hosts.root() }),
    ]);

  // Install fetches the binary and reads the live country list: up to three minutes.
  const installMut = useMutation({
    mutationFn: () => HttpUtil.post(`${BASE}/install`),
    onSettled: refresh,
  });

  const refreshRegionsMut = useMutation({
    mutationFn: () => HttpUtil.post(`${BASE}/regions/refresh`),
    onSettled: refresh,
  });

  const saveCountriesMut = useMutation({
    mutationFn: (codes: string[]) => HttpUtil.post(`${BASE}/countries`, { codes }, JSON_HEADERS),
    onSettled: refreshExits,
  });

  const saveInboundMut = useMutation({
    mutationFn: ({ inboundId, codes }: { inboundId: number; codes: string[] }) =>
      HttpUtil.post(`${BASE}/inbound/${inboundId}`, { codes }, JSON_HEADERS),
    onSettled: refreshExits,
  });

  const saveWarpMut = useMutation({
    mutationFn: (values: PsiphonWarpFormValues) =>
      HttpUtil.post(`${BASE}/warp`, values, JSON_HEADERS),
    onSettled: refresh,
  });

  // All is said outright: a body naming no country is refused, not read as every one.
  const resetTrafficMut = useMutation({
    mutationFn: (target: { code: string } | { all: true }) =>
      HttpUtil.post(`${BASE}/traffic/reset`, target, JSON_HEADERS),
    onSettled: refresh,
  });

  // Answers within about 30 s; the results come back in the state, under each country.
  const testMut = useMutation({
    mutationFn: () => HttpUtil.post(`${BASE}/test`),
    onSettled: refresh,
  });

  // Answers at once; the scan reports in the state, which is polled while it runs.
  const scanMut = useMutation({
    mutationFn: () => HttpUtil.post(`${BASE}/scan`),
    onSettled: refresh,
  });

  return {
    installMut,
    refreshRegionsMut,
    saveCountriesMut,
    saveInboundMut,
    saveWarpMut,
    resetTrafficMut,
    testMut,
    scanMut,
  };
}
