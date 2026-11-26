import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';

import type { ArvanCatalog, ArvanCreateState, ArvanServer } from '@/schemas/arvan';
import ArvanAddModal from '@/pages/relays/ArvanAddModal';
import { renderWithProviders } from './test-utils';

let keyStatus = { configured: true, readable: true };
let servers: ArvanServer[] = [];
const idle: ArvanCreateState = {
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
  steps: [],
};
let createState: ArvanCreateState = idle;
const catalog: ArvanCatalog = {
  zones: [
    { code: 'ir-thr-fr1', city: 'Tehran', dc: 'Foroogh', country: 'Iran' },
    { code: 'eu-west1-a', city: 'Karlsruhe', dc: 'Goethe', country: 'Germany' },
  ],
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
const mut = () => ({ mutateAsync: vi.fn(async () => ({ success: true })), isPending: false });
const createMut = mut();
const deleteServerMut = mut();
const saveKeyMut = mut();
const adoptMut = mut();

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
  useArvanDomains: (enabled: boolean) => ({
    data: enabled ? ['aliwoodbaf.ir', 'video-epic.ir'] : undefined,
  }),
  useArvanCreateState: () => ({ data: createState }),
  useArvanAccountMutations: () => ({ saveKeyMut, createMut, deleteServerMut, adoptMut }),
}));

function open() {
  renderWithProviders(<ArvanAddModal open onClose={vi.fn()} />);
}

function chooseOption(label: string, option: string) {
  const select = screen.getByLabelText(label).closest('.ant-select') as HTMLElement;
  fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
  const item = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
    (o) => (o.getAttribute('title') ?? o.textContent ?? '').trim() === option,
  );
  if (!item) throw new Error(`no option ${option} for ${label}`);
  fireEvent.click(item);
}

describe('the add-from-ArvanCloud window', () => {
  beforeEach(() => {
    keyStatus = { configured: true, readable: true };
    servers = [];
    createState = idle;
    vi.clearAllMocks();
  });
  afterEach(cleanup);

  it('asks for the key first, and offers nothing else without one', () => {
    keyStatus = { configured: false, readable: false };
    open();
    expect(screen.getByPlaceholderText(/apikey/)).toBeTruthy();
    expect(screen.queryByRole('tab')).toBeNull();
  });

  it('opens on the account’s servers, with the key kept and changeable', () => {
    open();
    const tabs = screen.getAllByRole('tab');
    expect(tabs[0].textContent).toBe('My servers');
    expect(tabs[0].getAttribute('aria-selected')).toBe('true');
    expect(screen.queryByPlaceholderText(/apikey/)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Change key' }));
    expect(screen.getByPlaceholderText(/apikey/)).toBeTruthy();
  });

  it('makes a server behind a record, on the tunnel chosen, only after the price is confirmed', async () => {
    open();
    fireEvent.click(screen.getByRole('tab', { name: 'New server' }));
    expect(screen.getByText(/13,860 an hour · 9,977,400 a month/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'ir 2' } });
    expect(
      (screen.getByRole('button', { name: /Make and add/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'ir2' } });
    // an Iranian zone cannot be an exit
    expect((screen.getByRole('radio', { name: 'Exit server' }) as HTMLInputElement).disabled).toBe(
      true,
    );
    chooseOption('Tunnel', 'Backhaul');
    chooseOption('Transport', 'wss');
    chooseOption('Domain', 'aliwoodbaf.ir');
    fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'IR2' } });
    expect(screen.getByText(/ir2\.aliwoodbaf\.ir will point at this server/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /Make and add/ }));
    expect(createMut.mutateAsync).not.toHaveBeenCalled();
    const confirm = await screen.findByText(/Make server ir2 in ArvanCloud\?/);
    const popover = confirm.closest('.ant-popover') as HTMLElement;
    expect(within(popover).getByText(/13,860 an hour \(9,977,400 a month\)/)).toBeTruthy();
    fireEvent.click(within(popover).getByRole('button', { name: /Make and add/ }));
    await waitFor(() =>
      expect(createMut.mutateAsync).toHaveBeenCalledWith({
        role: 'front',
        tool: 'backhaul',
        transport: 'wss',
        domain: 'aliwoodbaf.ir',
        record: 'ir2',
        zone: 'ir-thr-fr1',
        size: 'eco-1-1-0',
        image: 'u24',
        name: 'ir2',
      }),
    );
  });

  it('shows this run’s own steps and where it stopped', () => {
    createState = {
      ...idle,
      startedAt: 1,
      finishedAt: 2,
      name: 'spare',
      step: 'dns',
      ip: '37.152.190.77',
      error: 'could not be pointed at it',
      steps: ['add', 'setup', 'dns', 'bind'],
    };
    open();
    expect(screen.getByText('Server spare')).toBeTruthy();
    expect(screen.getByText('Point the domain at it')).toBeTruthy();
    expect(screen.queryByText('Make the server')).toBeNull();
    expect(screen.getByText('could not be pointed at it')).toBeTruthy();
  });

  it('adds a server already there with its password and the purpose chosen, and keeps one in use', async () => {
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
        zone: 'eu-west1-a',
        id: 'de1',
        name: 'de box',
        status: 'active',
        ips: ['185.226.1.9'],
        relay: '',
      },
    ];
    open();
    const [, inUse, free] = screen.getAllByRole('row');
    expect((within(inUse).getByRole('button', { name: 'Use' }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(
      (within(inUse).getByRole('button', { name: 'Delete' }) as HTMLButtonElement).disabled,
    ).toBe(true);

    fireEvent.click(within(free).getByRole('button', { name: 'Use' }));
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('de-box');
    const add = screen.getByRole('button', { name: 'Add' });
    expect((add as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Root password'), { target: { value: 'pw' } });
    // a zone outside Iran may be an exit
    fireEvent.click(screen.getByRole('radio', { name: 'Exit server' }));
    expect(screen.queryByLabelText('Tunnel')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() =>
      expect(adoptMut.mutateAsync).toHaveBeenCalledWith({
        role: 'exit',
        tool: 'backpack',
        transport: 'stealth',
        domain: '',
        record: '',
        zone: 'eu-west1-a',
        serverId: 'de1',
        name: 'de-box',
        password: 'pw',
      }),
    );
  });

  it('deletes a free server only after it is confirmed', async () => {
    servers = [
      {
        zone: 'ir-thr-fr1',
        id: 'free',
        name: 'spare',
        status: 'active',
        ips: ['37.152.190.77'],
        relay: '',
      },
    ];
    open();
    const [, row] = screen.getAllByRole('row');
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }));
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
