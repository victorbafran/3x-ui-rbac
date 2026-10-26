import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { getSharedWebSocketClient } from '@/api/websocket';
import { useWebSocketBridge } from '@/api/websocketBridge';
import { keys } from '@/api/queryKeys';

type ListenerMap = { listeners: Map<string, Set<(payload: unknown) => void>> };

describe('websocket bridge invalidate handler', () => {
  let originalWS: typeof WebSocket;

  beforeEach(() => {
    vi.useFakeTimers();
    originalWS = globalThis.WebSocket;
    class FakeWebSocket {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSING = 2;
      static readonly CLOSED = 3;
      readyState = FakeWebSocket.CONNECTING;
      addEventListener() {}
      removeEventListener() {}
      close() {}
      send() {}
    }
    globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  });

  afterEach(() => {
    globalThis.WebSocket = originalWS;
    vi.useRealTimers();
  });

  // Every inbound change sends inbounds then clients within milliseconds; one shared
  // debounce kept only the last, so other open panels never re-fetched their inbounds.
  it('re-fetches every type signalled inside one debounce window', () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(keys.inbounds.slim(), []);
    queryClient.setQueryData(keys.clients.all(), []);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    renderHook(() => useWebSocketBridge(), { wrapper });

    const client = getSharedWebSocketClient() as unknown as ListenerMap;
    const handlers = [...(client.listeners.get('invalidate') ?? [])];
    expect(handlers).toHaveLength(1);
    handlers[0]({ type: 'inbounds' });
    handlers[0]({ type: 'clients' });
    vi.advanceTimersByTime(200);

    expect(queryClient.getQueryState(keys.inbounds.slim())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(keys.clients.all())?.isInvalidated).toBe(true);
  });
});
