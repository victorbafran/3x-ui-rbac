import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';

import type { Relay } from '@/schemas/relay';
import { renderWithProviders } from './test-utils';

// The page carries the app sidebar, which needs a router and the settings
// query; neither is what these tests are about.
vi.mock('@/api/queries/useAllSettings', () => ({
  useAllSettings: () => ({ allSetting: {} }),
}));

let rows: Relay[] = [];

const relays: Relay[] = [
  {
    id: 1,
    name: 'netherlands',
    label: 'Netherlands',
    address: '198.51.100.7',
    sshPort: 22,
    sshUser: 'root',
    tunnel: 'wireguard',
    status: 'ready',
    lastError: '',
    lastSeen: 0,
    enabled: true,
    createdAt: 0,
    wgPeerPublic: 'pub',
    country: 'Netherlands',
    countryCode: 'NL',
    city: 'Amsterdam',
    interface: 'wgr1',
    tunnelUp: true,
    healthy: true,
    handshakeAgo: 12,
    received: 2048,
    sent: 1024,
    inUseBy: ['cisco-1'],
  },
  {
    id: 2,
    name: 'turkey',
    label: '',
    address: '198.51.100.8',
    sshPort: 2222,
    sshUser: 'root',
    tunnel: 'wireguard',
    status: 'error',
    lastError: 'cannot reach 198.51.100.8:2222',
    lastSeen: 0,
    enabled: true,
    createdAt: 0,
    wgPeerPublic: '',
    country: '',
    countryCode: '',
    city: '',
    interface: 'wgr2',
    tunnelUp: false,
    healthy: false,
    handshakeAgo: 0,
    received: 0,
    sent: 0,
    inUseBy: [],
  },
];

const removeMut = { mutateAsync: vi.fn(async () => ({ success: true })), isPending: false };

vi.mock('@/api/queries/useRelaysQuery', () => ({
  useRelaysQuery: () => ({
    relays: rows,
    isLoading: false,
    isFetched: true,
    error: null,
    refetch: vi.fn(),
  }),
  useRelayOptions: () => ({ options: [] }),
}));

vi.mock('@/api/queries/useRelayMutations', () => ({
  useRelayMutations: () => ({
    createMut: { mutateAsync: vi.fn(), isPending: false },
    updateMut: { mutateAsync: vi.fn(), isPending: false },
    removeMut,
    setEnableMut: { mutate: vi.fn(), isPending: false },
    probeMut: { mutateAsync: vi.fn(), isPending: false },
  }),
}));

const { default: RelaysPage } = await import('@/pages/relays/RelaysPage');

describe('the exit servers page', () => {
  beforeEach(() => {
    rows = relays;
  });

  // The operator has to be able to tell a working country from a broken one at
  // a glance; a page that lists both the same way is a page nobody checks.
  it('shows each server and says which one is in trouble', () => {
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    expect(screen.getByText('Netherlands')).toBeTruthy();
    expect(screen.getByText('turkey')).toBeTruthy();
    // 'Working' is also a summary heading, so the status is read off the row.
    const table = within(document.querySelector('.ant-table') as HTMLElement);
    expect(table.getByText('Working')).toBeTruthy();
    expect(table.getByText('Failed')).toBeTruthy();
    cleanup();
  });

  // The reason a server failed is the whole value of the row. Burying it
  // leaves the operator with a red tag and nothing to act on.
  it('shows why a server failed', () => {
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    expect(document.body.textContent).toContain('cannot reach 198.51.100.8:2222');
    cleanup();
  });

  // A country already offered by an inbound must say so, because removing it
  // would leave that country in every customer's dropdown leading nowhere.
  it('names the inbounds already offering a country', () => {
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    expect(screen.getByText('cisco-1')).toBeTruthy();
    cleanup();
  });

  // The first thing this page ever did on a real panel was crash: Go sends a
  // nil slice as null, zod's default() only fills an undefined, and parseMsg
  // hands the raw response straight to the component when validation fails.
  // A row with no inbounds behind it has to render.
  it('survives a server that no inbound is using yet', () => {
    rows = [{ ...relays[1], inUseBy: null } as unknown as Relay];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    expect(screen.getByText('turkey')).toBeTruthy();
    cleanup();
  });

  // The password is the part an operator worries about. The form has to say
  // what happens to it before they type it, not afterwards.
  it('says the password is used once before asking for it', () => {
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    expect(document.body.textContent).toContain('The password is used once');
    expect(document.body.textContent).toContain('never stored');
    cleanup();
  });

  // Deleting a server takes a tunnel off somebody's machine. It asks first,
  // and it says which one.
  it('asks before removing a server, naming it', async () => {
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const deleteButtons = document.querySelectorAll('button.ant-btn-dangerous');
    expect(deleteButtons.length).toBe(2);
    fireEvent.click(deleteButtons[0] as HTMLElement);
    await act(async () => {});
    expect(document.body.textContent).toContain('Remove Netherlands?');
    expect(removeMut.mutateAsync).not.toHaveBeenCalled();
    cleanup();
  });
});
