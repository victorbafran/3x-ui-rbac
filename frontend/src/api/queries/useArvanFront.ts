import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import type { Msg } from '@/utils';
import { keys } from '@/api/queryKeys';
import {
  ArvanCatalogSchema,
  ArvanCreateStateSchema,
  ArvanFrontViewSchema,
  ArvanKeyStatusSchema,
  ArvanServerSchema,
} from '@/schemas/arvan';
import type {
  ArvanCatalog,
  ArvanCreateRequest,
  ArvanCreateState,
  ArvanFrontFormValues,
  ArvanFrontView,
  ArvanKeyStatus,
  ArvanServer,
} from '@/schemas/arvan';

export type { ArvanFrontView, ArvanKeyStatus };

const BASE = '/panel/api/relays/arvan';
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchKeyStatus(): Promise<ArvanKeyStatus> {
  const msg = await HttpUtil.get(`${BASE}/key`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the Arvan key status');
  return ArvanKeyStatusSchema.parse(msg.obj ?? {});
}

export function useArvanKeyStatus(enabled: boolean) {
  return useQuery({ queryKey: keys.relays.arvan.key(), queryFn: fetchKeyStatus, enabled });
}

function viewOf(msg: Msg<unknown>, context: string): ArvanFrontView | null {
  if (!msg?.success || msg.obj == null) return null;
  const parsed = ArvanFrontViewSchema.safeParse(msg.obj);
  if (parsed.success) return parsed.data;
  console.warn(`[zod] ${context} response failed validation`, parsed.error.issues);
  return null;
}

async function fetchView(relayId: number): Promise<ArvanFrontView> {
  const msg = await HttpUtil.get(`${BASE}/front/${relayId}`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the Arvan front');
  const view = viewOf(msg, 'relays/arvan/front');
  if (!view) throw new Error('relays/arvan/front response failed validation');
  return view;
}

// Swaps and refills run on the server for a minute or more, so the view is read
// often while one runs and only now and then otherwise; never while closed.
export function useArvanFront(relayId: number, enabled: boolean) {
  return useQuery({
    queryKey: keys.relays.arvan.front(relayId),
    queryFn: () => fetchView(relayId),
    enabled: enabled && relayId > 0,
    refetchInterval: (q) => (q.state.data?.busy ? 3000 : 15000),
  });
}

export type ArvanStartedAction = 'prepare' | 'refill' | 'test' | 'swap' | 'release';

// These return once the work has started; the view's busy label and the polling
// above carry it from there.
function useStartedMutation(relayId: number, action: ArvanStartedAction) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body?: { ip?: string }) =>
      HttpUtil.post(`${BASE}/front/${relayId}/${action}`, body ?? {}, JSON_HEADERS),
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.relays.arvan.front(relayId) }),
  });
}

export function useArvanMutations(relayId: number) {
  const queryClient = useQueryClient();
  const frontKey = keys.relays.arvan.front(relayId);
  const refreshAll = () => queryClient.invalidateQueries({ queryKey: keys.relays.arvan.root() });
  const putView = (msg: Msg<unknown>, context: string) => {
    const view = viewOf(msg, context);
    if (view) queryClient.setQueryData(frontKey, view);
    else void queryClient.invalidateQueries({ queryKey: frontKey });
    return view;
  };

  const saveKeyMut = useMutation({
    mutationFn: (key: string) => HttpUtil.post(`${BASE}/key`, { key }, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) void refreshAll();
    },
  });

  const clearKeyMut = useMutation({
    mutationFn: () => HttpUtil.post(`${BASE}/key/clear`),
    onSuccess: (msg) => {
      if (msg?.success) void refreshAll();
    },
  });

  const detectMut = useMutation({
    mutationFn: async () => {
      const msg = await HttpUtil.post(`${BASE}/front/${relayId}/detect`);
      return msg?.success ? putView(msg, 'relays/arvan/detect') : null;
    },
  });

  const saveMut = useMutation({
    mutationFn: async (values: ArvanFrontFormValues) => {
      const msg = await HttpUtil.post(`${BASE}/front/${relayId}/save`, values, JSON_HEADERS);
      return msg?.success ? putView(msg, 'relays/arvan/save') : null;
    },
  });

  return {
    saveKeyMut,
    clearKeyMut,
    detectMut,
    saveMut,
    prepareMut: useStartedMutation(relayId, 'prepare'),
    refillMut: useStartedMutation(relayId, 'refill'),
    testMut: useStartedMutation(relayId, 'test'),
    swapMut: useStartedMutation(relayId, 'swap'),
    releaseMut: useStartedMutation(relayId, 'release'),
  };
}

// The account side: the key, what a zone offers, the servers there, and making a new entry
// server. Nothing here is read until the window is open.

async function fetchCatalog(zone: string): Promise<ArvanCatalog> {
  const msg = await HttpUtil.get(`${BASE}/catalog`, zone ? { zone } : undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read what ArvanCloud offers');
  return ArvanCatalogSchema.parse(msg.obj ?? {});
}

export function useArvanCatalog(zone: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.relays.arvan.catalog(zone),
    queryFn: () => fetchCatalog(zone),
    enabled,
  });
}

async function fetchServers(): Promise<ArvanServer[]> {
  const msg = await HttpUtil.get(`${BASE}/servers`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to list the ArvanCloud servers');
  return ArvanServerSchema.array().parse(msg.obj ?? []);
}

export function useArvanServers(enabled: boolean) {
  return useQuery({ queryKey: keys.relays.arvan.servers(), queryFn: fetchServers, enabled });
}

async function fetchCreateState(): Promise<ArvanCreateState> {
  const msg = await HttpUtil.get(`${BASE}/create`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the new server');
  return ArvanCreateStateSchema.parse(msg.obj ?? {});
}

// Read every 3 s while a server is being made, and not otherwise.
export function useArvanCreateState(enabled: boolean) {
  return useQuery({
    queryKey: keys.relays.arvan.create(),
    queryFn: fetchCreateState,
    enabled,
    refetchInterval: (q) => (q.state.data?.running ? 3000 : false),
  });
}

export function useArvanAccountMutations() {
  const queryClient = useQueryClient();
  const refreshAll = () => queryClient.invalidateQueries({ queryKey: keys.relays.arvan.root() });
  const saveKeyMut = useMutation({
    mutationFn: (key: string) => HttpUtil.post(`${BASE}/key`, { key }, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) void refreshAll();
    },
  });
  const createMut = useMutation({
    mutationFn: (req: ArvanCreateRequest) => HttpUtil.post(`${BASE}/create`, req, JSON_HEADERS),
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.relays.arvan.create() }),
  });
  const deleteServerMut = useMutation({
    mutationFn: (server: { zone: string; id: string }) =>
      HttpUtil.post(`${BASE}/servers/delete`, server, JSON_HEADERS),
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.relays.arvan.servers() }),
  });
  return { saveKeyMut, createMut, deleteServerMut };
}
