import { useMutation, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import type { AdminPayload } from '@/schemas/api/admin';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

export function useAdminMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: keys.admins.root() });

  const addMut = useMutation({
    mutationFn: (payload: AdminPayload) =>
      HttpUtil.post('/panel/api/admins/add', payload, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: AdminPayload }) =>
      HttpUtil.post(`/panel/api/admins/update/${id}`, payload, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const resetPasswordMut = useMutation({
    mutationFn: ({ id, password }: { id: number; password: string }) =>
      HttpUtil.post(`/panel/api/admins/resetPassword/${id}`, { password }, JSON_HEADERS),
  });

  const removeMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/admins/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  return {
    add: addMut.mutateAsync,
    update: (id: number, payload: AdminPayload) => updateMut.mutateAsync({ id, payload }),
    resetPassword: (id: number, password: string) => resetPasswordMut.mutateAsync({ id, password }),
    remove: removeMut.mutateAsync,
    saving: addMut.isPending || updateMut.isPending || resetPasswordMut.isPending,
  };
}
