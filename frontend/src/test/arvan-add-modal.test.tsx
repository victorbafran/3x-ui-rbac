import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';

import type { ArvanCatalog, ArvanCreateState, ArvanServer } from '@/schemas/arvan';
import ArvanAddModal from '@/pages/relays/ArvanAddModal';
import { renderWithProviders } from './test-utils';

let keyStatus = { configured: true, readable: true };
let servers: ArvanServer[] = [];
let createState: ArvanCreateState = {
  running: false,
  step: '',
  error: '',
  name: '',
  zone: '',
  serverId: '',
  ip: '',
  relayId: 0,
  startedAt: 0,
  finishedAt: 0,
};
const catalog: ArvanCatalog = {
  zones: [{ code: 'ir-thr-fr1', city: 'Tehran', dc: 'Foroogh', country: 'Iran' }],
  zone: 'ir-thr-fr1',
  sizes: [
    {
      id: 'eco-1-1-0',
      name: 'eco-small1',
      cpus: 1,
      memoryGb: 1,
      diskGb: 25,
      pricePerHour: 13860,
      pricePerMonth: 9977400,
      type: 'economic',
    },
  ],
  images: [{ id: 'u24', distribution: 'ubuntu', version: '24.04' }],
  serverCount: 0,
  serverLimit: 5,
  defaultImage: 'u24',
};
const createMut = { mutateAsync: vi.fn(async () => ({ success: true })), isPending: false };
const deleteServerMut = { mutateAsync: vi.fn(async () => ({ success: true })), isPending: false };
const saveKeyMut = { mutateAsync: vi.fn(async () => ({ success: true })), isPending: false };

vi.mock('@/api/queries/useArvanFront', () => ({
  useArvanKeyStatus: () => ({ data: keyStatus }),
  useArvanCatalog: (_zone: string, enabled: boolean) => ({
    data: enabled ? catalog : undefined,
    isFetching: false,
    error: null,
  }),
  useArvanServers: (enabled: boolean) => ({
    data: enabled ? servers : undefined,
    isLoading: false,
    isFetching: false,
    error: null,
    refetch: vi.fn(),
  }),
  useArvanCreateState: () => ({ data: createState }),
  useArvanAccountMutations: () => ({ saveKeyMut, createMut, deleteServerMut }),
}));

function open(onUseServer = vi.fn()) {
  renderWithProviders(<ArvanAddModal open onClose={vi.fn()} onUseServer={onUseServer} />);
  return onUseServer;
}

describe('the add-from-ArvanCloud window', () => {
  beforeEach(() => {
    keyStatus = { configured: true, readable: true };
    servers = [];
    createState = {
      ...createState,
      running: false,
      step: '',
      error: '',
      startedAt: 0,
      finishedAt: 0,
    };
    vi.clearAllMocks();
  });
  afterEach(cleanup);

  it('asks for the key first, and offers nothing else without one', () => {
    keyStatus = { configured: false, readable: false };
    open();
    expect(screen.getByPlaceholderText(/apikey/)).toBeTruthy();
    expect(screen.queryByRole('tab')).toBeNull();
  });

  it('makes a server only after the price is confirmed, with the zone’s own choices', async () => {
    open();
    expect(screen.getByText(/13,860 an hour · 9,977,400 a month/)).toBeTruthy();
    const create = screen.getByRole('button', { name: /Make and add/ });
    expect((create as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'ir 2' } });
    expect(
      (screen.getByRole('button', { name: /Make and add/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'ir2' } });
    fireEvent.click(screen.getByRole('button', { name: /Make and add/ }));
    expect(createMut.mutateAsync).not.toHaveBeenCalled();
    const confirm = await screen.findByText(/Make server ir2 in ArvanCloud\?/);
    const popover = confirm.closest('.ant-popover') as HTMLElement;
    expect(within(popover).getByText(/13,860 an hour \(9,977,400 a month\)/)).toBeTruthy();
    fireEvent.click(within(popover).getByRole('button', { name: /Make and add/ }));
    await waitFor(() =>
      expect(createMut.mutateAsync).toHaveBeenCalledWith({
        zone: 'ir-thr-fr1',
        size: 'eco-1-1-0',
        image: 'u24',
        name: 'ir2',
      }),
    );
  });

  it('shows where a make stopped, and its error', () => {
    createState = {
      ...createState,
      startedAt: 1,
      finishedAt: 2,
      name: 'ir2',
      step: 'ssh',
      ip: '37.152.190.9',
      error: 'did not take the panel’s key',
    };
    open();
    expect(screen.getByText('Server ir2')).toBeTruthy();
    expect(screen.getByText('37.152.190.9')).toBeTruthy();
    expect(screen.getByText('did not take the panel’s key')).toBeTruthy();
  });

  it('lets a free server be used or deleted, and keeps one in use on this panel', async () => {
    servers = [
      {
        zone: 'ir-thr-fr1',
        id: 'old',
        name: 'front',
        status: 'active',
        ips: ['37.152.187.164'],
        relay: 'Ir',
      },
      {
        zone: 'ir-thr-fr1',
        id: 'free',
        name: 'spare',
        status: 'active',
        ips: ['37.152.190.77'],
        relay: '',
      },
    ];
    const onUse = open();
    fireEvent.click(screen.getByRole('tab', { name: 'My servers' }));
    const [, inUse, free] = screen.getAllByRole('row');
    expect(
      (within(inUse).getByRole('button', { name: 'Use as entry server' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect(
      (within(inUse).getByRole('button', { name: 'Delete' }) as HTMLButtonElement).disabled,
    ).toBe(true);

    fireEvent.click(within(free).getByRole('button', { name: 'Use as entry server' }));
    expect(onUse).toHaveBeenCalledWith('37.152.190.77', 'spare');

    fireEvent.click(within(free).getByRole('button', { name: 'Delete' }));
    expect(deleteServerMut.mutateAsync).not.toHaveBeenCalled();
    const confirm = await screen.findByText('Delete spare from ArvanCloud?');
    fireEvent.click(
      within(confirm.closest('.ant-popover') as HTMLElement).getByRole('button', {
        name: 'Delete',
      }),
    );
    await waitFor(() =>
      expect(deleteServerMut.mutateAsync).toHaveBeenCalledWith({ zone: 'ir-thr-fr1', id: 'free' }),
    );
  });
});
