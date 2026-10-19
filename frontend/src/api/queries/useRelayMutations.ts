import { useMutation, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import { ReachabilityMsgSchema } from '@/schemas/relay';
import type { Reachability, RelayFormValues } from '@/schemas/relay';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

export function useRelayMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: keys.relays.root() });

  const createMut = useMutation({
    mutationFn: (payload: RelayFormValues) =>
      HttpUtil.post('/panel/api/relays/add', payload, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: RelayFormValues }) =>
      HttpUtil.post(`/panel/api/relays/update/${id}`, payload, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const removeMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/relays/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const setEnableMut = useMutation({
    mutationFn: ({ id, enable }: { id: number; enable: boolean }) =>
      HttpUtil.post(`/panel/api/relays/setEnable/${id}`, { enable }, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  // Asks the relay itself what the internet gives it. Nothing is invalidated:
  // this changes no state, it only reports what that address can reach.
  const probeMut = useMutation<Reachability | null, Error, number>({
    mutationFn: async (id: number) => {
      const msg = await HttpUtil.post(`/panel/api/relays/probe/${id}`);
      if (!msg?.success) throw new Error(msg?.msg || 'probe failed');
      return ReachabilityMsgSchema.parse(msg).obj;
    },
  });

  return { createMut, updateMut, removeMut, setEnableMut, probeMut };
}
