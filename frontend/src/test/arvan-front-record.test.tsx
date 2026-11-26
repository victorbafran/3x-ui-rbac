import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';

import ArvanFrontModal from '@/pages/relays/ArvanFrontModal';
import { ArvanFrontViewSchema } from '@/schemas/arvan';
import type { Relay } from '@/schemas/relay';
import { renderWithProviders } from './test-utils';

const view = ArvanFrontViewSchema.parse({
  relayId: 7,
  relayName: 'ir2',
  keyConfigured: true,
  keyReadable: true,
  bound: true,
  zone: 'ir-thr-fr1',
  serverId: 'srv',
  serverName: 'ir2',
  domain: 'aliwoodbaf.ir',
  recordName: 'milichi',
  recordId: 'r-milichi',
  recordIp: '37.152.190.9',
  activeIp: '37.152.190.9',
  prepared: true,
});
const idle = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(async () => view), isPending: false });
const recordMut = idle();

vi.mock('@/api/queries/useArvanFront', () => ({
  useArvanKeyStatus: () => ({ data: { configured: true, readable: true } }),
  useArvanFront: () => ({ data: view, error: null, isLoading: false }),
  useArvanDomains: () => ({ data: ['aliwoodbaf.ir', 'video-epic.ir'], isFetching: false }),
  useArvanMutations: () => ({
    saveKeyMut: idle(),
    clearKeyMut: idle(),
    detectMut: idle(),
    recordMut,
    saveMut: idle(),
    prepareMut: idle(),
    refillMut: idle(),
    testMut: idle(),
    swapMut: idle(),
    releaseMut: idle(),
  }),
}));

const relay = { id: 7, name: 'ir2', address: '37.152.190.9', role: 'front' } as Relay;

describe('a front’s Arvan window', () => {
  afterEach(cleanup);

  it('points a chosen record at the front, offering the domain it is bound to', async () => {
    renderWithProviders(<ArvanFrontModal relay={relay} onClose={vi.fn()} />);
    const point = screen.getByRole('button', { name: 'Point at this server' }) as HTMLButtonElement;
    expect(point.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'IR 5' } });
    expect(
      (screen.getByRole('button', { name: 'Point at this server' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'ir5' } });
    expect(screen.getByText(/ir5\.aliwoodbaf\.ir will point at 37\.152\.190\.9/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Point at this server' }));
    await waitFor(() =>
      expect(recordMut.mutateAsync).toHaveBeenCalledWith({
        domain: 'aliwoodbaf.ir',
        record: 'ir5',
      }),
    );
  });
});
