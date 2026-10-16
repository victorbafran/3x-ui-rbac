import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { AdminMeSchema, type AdminMe } from '@/generated/zod';
import { keys } from '@/api/queryKeys';

export type { AdminMe };

async function fetchMe(): Promise<AdminMe> {
  const msg = await HttpUtil.get('/panel/api/admins/me', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch current admin');
  return parseMsg(msg, AdminMeSchema, 'admins/me').obj as AdminMe;
}

// The sidebar hides on these switches, so the result is kept fresh but never
// refetched on focus: a flicker of the full menu would read as a leak.
export function useMeQuery() {
  const query = useQuery({
    queryKey: keys.admins.me(),
    queryFn: fetchMe,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });
  return {
    me: query.data,
    loading: query.isLoading,
    can: (permission: string) => Boolean(query.data?.permissions?.[permission]),
  };
}
