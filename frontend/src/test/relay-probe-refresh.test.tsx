import type { ReactNode } from 'react';
import { act, renderHook } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useRelayMutations } from '@/api/queries/useRelayMutations';
import { keys } from '@/api/queryKeys';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('probing a relay', () => {
  // A probe reads a front's IPv6 off it and dials it; a list kept from before
  // the probe hid the address it had just found until the next idle poll.
  it('refreshes the relay list, which the probe may have written to', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', { Address: '203.0.113.9', Country: 'IR' }),
    );
    const queryClient = makeTestQueryClient();
    const fetchList = vi
      .fn()
      .mockResolvedValueOnce([{ id: 3, address6: '' }])
      .mockResolvedValueOnce([{ id: 3, address6: '2001:db8::5' }]);
    const listQuery = { queryKey: keys.relays.list(), queryFn: fetchList, staleTime: Infinity };
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );

    await queryClient.query(listQuery);
    const { result } = renderHook(() => useRelayMutations(), { wrapper });
    await act(async () => {
      await result.current.probeMut.mutateAsync(3);
    });

    expect(await queryClient.query(listQuery)).toEqual([{ id: 3, address6: '2001:db8::5' }]);
  });
});
