import { useMemo } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import { PanelCloneListSchema, StandbyStateSchema, cloneIsWorking } from '@/schemas/clone';
import type { CloneFormValues, PanelClone, StandbyState } from '@/schemas/clone';

export type { PanelClone, StandbyState };

const BASE = '/panel/api/clones';
const STANDBY_URL = '/panel/api/server/standby';
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchClones(): Promise<PanelClone[]> {
  const msg = await HttpUtil.get(`${BASE}/list`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch panel copies');
  return PanelCloneListSchema.parse(msg.obj);
}

// Installing a copy takes many minutes on the server, so the list is read
// often while one is working and only now and then otherwise.
export function useClonesQuery() {
  const query = useQuery({
    queryKey: keys.clones.list(),
    queryFn: fetchClones,
    refetchInterval: (q) => (q.state.data?.some(cloneIsWorking) ? 3000 : 20000),
  });
  const clones = useMemo(() => query.data ?? [], [query.data]);
  return { ...query, clones };
}

export type CloneAction = 'sync' | 'check' | 'promote' | 'del';

// Sync and promote return once the work has started; the row's step and the
// polling above carry them from there.
function useCloneAction(action: CloneAction) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => HttpUtil.post(`${BASE}/${action}/${id}`),
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.clones.root() }),
  });
}

export function useCloneMutations() {
  const queryClient = useQueryClient();
  const addMut = useMutation({
    mutationFn: (values: CloneFormValues) => HttpUtil.post(`${BASE}/add`, values, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) void queryClient.invalidateQueries({ queryKey: keys.clones.root() });
    },
  });
  return {
    addMut,
    syncMut: useCloneAction('sync'),
    checkMut: useCloneAction('check'),
    promoteMut: useCloneAction('promote'),
    removeMut: useCloneAction('del'),
  };
}

async function fetchStandby(): Promise<StandbyState> {
  const msg = await HttpUtil.get(STANDBY_URL, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the standby state');
  return StandbyStateSchema.parse(msg.obj ?? {});
}

// The route is super-admin only; mount its readers only for a super admin.
export function useStandbyQuery(refetchInterval: number | false = false) {
  return useQuery({ queryKey: keys.server.standby(), queryFn: fetchStandby, refetchInterval });
}

// The panel restarts its own process a second after the switch, so a success is
// written straight into the cache; a failure may still have moved the marker.
export function useSetStandbyMutation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (standby: boolean) => HttpUtil.post(STANDBY_URL, { standby }, JSON_HEADERS),
    onSuccess: (msg, standby) => {
      if (msg?.success) queryClient.setQueryData<StandbyState>(keys.server.standby(), { standby });
      else void queryClient.invalidateQueries({ queryKey: keys.server.standby() });
    },
  });
}
