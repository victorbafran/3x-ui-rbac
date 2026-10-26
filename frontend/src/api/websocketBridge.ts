import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';

import { getSharedWebSocketClient } from '@/api/websocket';
import { keys } from '@/api/queryKeys';
import { isRecentLocalInvalidate } from '@/api/invalidationTracker';

type Handler = (payload: unknown) => void;
type InvalidatedRoot = 'inbounds' | 'clients';

let invalidateTimer: number | null = null;
// An inbound change signals inbounds then clients at once; each must be re-fetched.
const pendingRoots = new Set<InvalidatedRoot>();

export function useWebSocketBridge() {
  const queryClient = useQueryClient();

  useEffect(() => {
    const client = getSharedWebSocketClient();

    const onInvalidate: Handler = (payload) => {
      const p = payload as { type?: string } | undefined;
      if (!p || (p.type !== 'inbounds' && p.type !== 'clients')) return;
      pendingRoots.add(p.type);
      if (invalidateTimer != null) clearTimeout(invalidateTimer);
      invalidateTimer = window.setTimeout(() => {
        invalidateTimer = null;
        const roots = [...pendingRoots];
        pendingRoots.clear();
        if (isRecentLocalInvalidate()) return;
        for (const root of roots) {
          queryClient.invalidateQueries({ queryKey: [root] });
        }
      }, 200);
    };

    const onOutbounds: Handler = (payload) => {
      if (!Array.isArray(payload)) return;
      queryClient.setQueryData(keys.xray.outboundsTraffic(), payload);
    };

    const onNodes: Handler = (payload) => {
      if (!Array.isArray(payload)) return;
      queryClient.setQueryData(keys.nodes.list(), payload);
    };

    client.on('invalidate', onInvalidate);
    client.on('outbounds', onOutbounds);
    client.on('nodes', onNodes);
    client.connect();

    return () => {
      client.off('invalidate', onInvalidate);
      client.off('outbounds', onOutbounds);
      client.off('nodes', onNodes);
      if (invalidateTimer != null) {
        clearTimeout(invalidateTimer);
        invalidateTimer = null;
      }
      pendingRoots.clear();
    };
  }, [queryClient]);
}
