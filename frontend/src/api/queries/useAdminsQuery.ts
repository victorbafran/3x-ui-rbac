import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import {
  AdminViewSchema,
  PermissionCatalogSchema,
  type AdminView,
  type PermissionCatalog,
} from '@/generated/zod';
import { keys } from '@/api/queryKeys';

export type { AdminView, PermissionCatalog };

const AdminListSchema = z.array(AdminViewSchema);

async function fetchAdmins(): Promise<AdminView[]> {
  const msg = await HttpUtil.get('/panel/api/admins/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch admins');
  const validated = parseMsg(msg, AdminListSchema, 'admins/list');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

async function fetchCatalog(): Promise<PermissionCatalog> {
  const msg = await HttpUtil.get('/panel/api/admins/permissions', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch permission catalog');
  return parseMsg(msg, PermissionCatalogSchema, 'admins/permissions').obj as PermissionCatalog;
}

export function useAdminsQuery() {
  const query = useQuery({ queryKey: keys.admins.list(), queryFn: fetchAdmins });
  const admins = useMemo(() => query.data ?? [], [query.data]);
  return {
    admins,
    loading: query.isFetching,
    fetched: query.data !== undefined || query.isError,
    fetchError: query.error ? (query.error as Error).message : '',
    refetch: query.refetch,
  };
}

// The catalog is the server's closed key set; it changes only with a deploy.
export function usePermissionCatalog() {
  const query = useQuery({
    queryKey: keys.admins.catalog(),
    queryFn: fetchCatalog,
    staleTime: Infinity,
  });
  return { catalog: query.data, loading: query.isLoading };
}
