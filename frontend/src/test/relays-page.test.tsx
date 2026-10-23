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
    role: 'exit',
    tunnelPort: 0,
    transport: '',
    tunnelDetail: '',
    forwardedPorts: [],
    proxyPorts: [],
    publicName: '',
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
    role: 'exit',
    tunnelPort: 0,
    transport: '',
    tunnelDetail: '',
    forwardedPorts: [],
    proxyPorts: [],
    publicName: '',
  },
];

// A front in Iran that the panel's client is connected to, carrying four of
// the panel's inbound ports.
const front: Relay = {
  ...relays[1],
  id: 3,
  name: 'iran-front',
  label: 'Iran',
  address: '203.0.113.9',
  sshPort: 22,
  tunnel: 'backhaul',
  status: 'ready',
  lastError: '',
  interface: '',
  tunnelUp: true,
  healthy: true,
  inUseBy: [],
  role: 'front',
  tunnelPort: 3080,
  transport: 'tcp',
  tunnelDetail: '',
  forwardedPorts: [1194, 10434, 41330],
  proxyPorts: [],
  publicName: 'tun.example.ir',
};

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

  // An entry server is a different kind of thing from an exit, and the row
  // has to say so: it shows the ports customers can reach through it rather
  // than WireGuard counters it does not have.
  it('marks an entry server and lists the ports it carries', () => {
    rows = [relays[0], front];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const table = within(document.querySelector('.ant-table') as HTMLElement);
    expect(table.getByText('Entry')).toBeTruthy();
    expect(table.getByText('Exit')).toBeTruthy();
    expect(document.body.textContent).toContain('Forwards 1194, 10434, 41330');
    expect(document.body.textContent).toContain('tcp:3080');
    cleanup();
  });

  // A front whose client is alive but holds no connection must not look
  // healthy, and must not be described with WireGuard's words either.
  it('says an entry server is not connected rather than calling it silent', () => {
    rows = [{ ...front, healthy: false }];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const table = within(document.querySelector('.ant-table') as HTMLElement);
    expect(table.getByText('Not connected')).toBeTruthy();
    expect(table.queryByText('No handshake')).toBeNull();
    expect(table.queryByText('Working')).toBeNull();
    cleanup();
  });

  // When the panel's own end is down, what the client last said is the only
  // clue the operator has.
  it('shows why the panel is not dialling an entry server', () => {
    rows = [
      { ...front, tunnelUp: false, healthy: false, tunnelDetail: 'backhaul exited: bind refused' },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    expect(document.body.textContent).toContain('backhaul exited: bind refused');
    cleanup();
  });

  // The operator opens the front's firewall from this row, so it names the
  // tool and every port the tunnel listens on: Backhaul's UDP leg one port up,
  // and nothing extra for BackPack, which carries UDP on the one tunnel.
  it('names the tunnel tool and every port it listens on', () => {
    rows = [
      front,
      { ...front, id: 4, name: 'iran-bp', label: 'Iran BP', tunnel: 'backpack', tunnelPort: 3090 },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const text = document.body.textContent ?? '';
    expect(text).toContain('tun.example.ir');
    expect(text).toContain('backhaul tcp:3080 + udp:3081');
    expect(text).toContain('backpack tcp:3090');
    expect(text).not.toContain('backpack tcp:3090 + udp');
    cleanup();
  });

  // Real-IP ports ride BackPack's second tunnel, whose port the firewall needs
  // too; a Backhaul front cannot carry them, and the row must say so.
  it('shows where the real-IP ports go, and where they cannot', () => {
    rows = [
      {
        ...front,
        id: 4,
        name: 'iran-bp',
        label: 'Iran BP',
        tunnel: 'backpack',
        tunnelPort: 3090,
        proxyPorts: [443],
      },
      { ...front, id: 5, name: 'iran-bh', label: 'Iran BH', proxyPorts: [8443] },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const text = document.body.textContent ?? '';
    expect(text).toContain('backpack tcp:3090 + proxy:3091');
    expect(text).toContain('Real client IP: 443');
    expect(text).toContain('Not carried: 8443. Real client IP needs a BackPack entry server.');
    expect(text).not.toContain('Real client IP: 8443');
    cleanup();
  });

  // Backhaul needs a second port for UDP and the form says so before the
  // server is added; BackPack does not, so the note goes when it is chosen.
  it('tells the operator about the UDP port only for Backhaul', async () => {
    rows = relays;
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    await act(async () => {});
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    expect(document.body.textContent).toContain('Backhaul also uses port 3081');
    fireEvent.click(screen.getByText('BackPack'));
    await act(async () => {});
    expect(document.body.textContent).not.toContain('Backhaul also uses port');
    cleanup();
  });

  // A new BackPack front starts on stealth, and going back to Backhaul, which
  // has no stealth, falls back to tcp rather than a transport it would refuse.
  it('starts a BackPack entry server on stealth', async () => {
    rows = relays;
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    await act(async () => {});
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    const shown = () => document.querySelector('#transport')?.closest('.ant-select')?.textContent;
    expect(shown()).toBe('tcp');
    fireEvent.click(screen.getByText('BackPack'));
    await act(async () => {});
    expect(shown()).toBe('stealth');
    fireEvent.click(screen.getByText('Backhaul'));
    await act(async () => {});
    expect(shown()).toBe('tcp');
    cleanup();
  });

  // The tunnel's port and transport only mean something for an entry server,
  // so they appear when that is chosen and not before.
  it('asks for the tunnel settings only when adding an entry server', async () => {
    rows = relays;
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    await act(async () => {});
    expect(document.body.textContent).not.toContain('Tunnel port');
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    expect(document.body.textContent).toContain('Tunnel port');
    expect(document.body.textContent).toContain('Tunnel transport');
    expect(document.body.textContent).toContain('Customers connect to this server’s address');
    cleanup();
  });
});
