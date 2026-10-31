import { useEffect, useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';

import type { Relay } from '@/schemas/relay';
import {
  psiphonRefetchMs,
  type PsiphonCountryUse,
  type PsiphonExitTest,
  type PsiphonInbound,
  type PsiphonState,
} from '@/schemas/psiphon';
import { chooseSelectOption, fieldLabels, renderWithProviders } from './test-utils';

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
    publicName6: '',
    address6: '',
    ipFamily: '',
    address6Error: '',
    trafficIn: 0,
    trafficOut: 0,
    rateIn: 0,
    rateOut: 0,
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
    publicName6: '',
    address6: '',
    ipFamily: '',
    address6Error: '',
    trafficIn: 0,
    trafficOut: 0,
    rateIn: 0,
    rateOut: 0,
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
const updateMut = { mutateAsync: vi.fn(async () => ({ success: true })), isPending: false };

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
    updateMut,
    removeMut,
    setEnableMut: { mutate: vi.fn(), isPending: false },
    probeMut: { mutateAsync: vi.fn(), isPending: false },
  }),
}));

let psiphonState: PsiphonState | undefined;
let psiphonInbounds: PsiphonInbound[] = [];
const installMut = { mutate: vi.fn(), isPending: false };
const saved = async (_payload: unknown) => ({ success: true });
const saveCountriesMut = { mutateAsync: vi.fn(saved), isPending: false };
const saveInboundMut = { mutateAsync: vi.fn(saved), isPending: false, variables: undefined };
const saveWarpMut = { mutateAsync: vi.fn(saved), isPending: false };
const resetTrafficMut = { mutateAsync: vi.fn(saved), isPending: false };
const testMut = { mutate: vi.fn(), isPending: false };
const scanMut = { mutate: vi.fn(), isPending: false };

vi.mock('@/api/queries/usePsiphon', () => ({
  usePsiphonState: () => ({
    data: psiphonState,
    isLoading: false,
    isFetching: false,
    error: null,
    refetch: vi.fn(),
  }),
  usePsiphonInbounds: () => ({ data: psiphonInbounds, isLoading: false, error: null }),
  usePsiphonMutations: () => ({
    installMut,
    refreshRegionsMut: { mutate: vi.fn(), isPending: false },
    saveCountriesMut,
    saveInboundMut,
    saveWarpMut,
    resetTrafficMut,
    testMut,
    scanMut,
  }),
}));

const { default: RelaysPage } = await import('@/pages/relays/RelaysPage');

// The transport options, read by toggling the select: listSelectOptions closes
// it with Escape, which closes the whole modal and freezes what it shows.
function transportOptions(): string[] {
  const select = document.querySelector('#transport')?.closest('.ant-select') as HTMLElement;
  fireEvent.mouseDown(select);
  const shown = Array.from(
    document.querySelectorAll(
      '.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option',
    ),
  ).map((o) => (o.getAttribute('title') ?? o.textContent ?? '').trim());
  fireEvent.mouseDown(select);
  return shown;
}

describe('the exit servers page', () => {
  beforeEach(() => {
    rows = relays;
    psiphonState = undefined;
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

  // What customers moved through a front, with the speed under it only while it
  // has one; a front with nothing counted yet keeps its row to the ports.
  it('shows what customers moved through an entry server, and how fast right now', async () => {
    rows = [
      {
        ...front,
        tunnel: 'backpack',
        trafficOut: 699355553,
        trafficIn: 75264133,
        rateOut: 1572864,
        rateIn: 51200,
      },
      { ...front, id: 4, name: 'iran-idle', tunnel: 'backpack', trafficOut: 2048 },
      { ...front, id: 5, name: 'iran-bh' },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = (id: number) => document.querySelector(`tr[data-row-key="${id}"]`) as HTMLElement;
    expect(row(3).textContent).toContain('↓ 667 MB · ↑ 72 MB');
    expect(row(3).textContent).toContain('↓ 1.5 MB/s · ↑ 50 KB/s');
    expect(row(3).textContent).toContain('Forwards 1194, 10434, 41330');
    expect(row(4).textContent).toContain('↓ 2.0 KB · ↑ 0');
    expect(row(4).textContent).not.toContain('/s');
    expect(row(5).textContent).not.toContain('↓');
    expect(row(5).textContent).toContain('Forwards 1194, 10434, 41330');
    fireEvent.mouseEnter(within(row(3)).getByText('↓ 667 MB · ↑ 72 MB'));
    await waitFor(() =>
      expect(document.body.textContent).toContain(
        'Customer traffic through this entry server since it was added',
      ),
    );
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
    expect(shown()).toBe('stealth — encrypted');
    fireEvent.click(screen.getByText('Backhaul'));
    await act(async () => {});
    expect(shown()).toBe('tcp');
    cleanup();
  });

  // udp as the main tunnel turns every customer port into a UDP-only listener,
  // so a new entry server is not offered it; one saved on it keeps it on edit.
  it('offers udp only to an entry server already on it', async () => {
    rows = [{ ...front, transport: 'udp' }];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    await act(async () => {});
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    let options = transportOptions();
    expect(options).toContain('udpovertcp');
    expect(options).not.toContain('udp');
    fireEvent.click(screen.getByText('BackPack'));
    await act(async () => {});
    options = transportOptions();
    expect(options).toContain('stealth — encrypted');
    expect(options).not.toContain('udp');
    cleanup();

    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(
      document.querySelector('[aria-label="edit"]')?.closest('button') as HTMLElement,
    );
    await act(async () => {});
    expect(transportOptions()).toContain('udp');
    cleanup();
  });

  // The owner asked whether the tunnel is secure: only the marked transports
  // encrypt it, and the form says so while an unencrypted one is chosen.
  it('marks the encrypted transports and warns while an unencrypted one is chosen', async () => {
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
    // Every transport, in order: the spec measured the token readable on each
    // unmarked one, so marking one more would tell the owner a lie.
    expect(transportOptions()).toEqual([
      'tcp',
      'tcpmux',
      'ws',
      'wsmux',
      'wss — encrypted',
      'wssmux — encrypted',
      'udpovertcp',
    ]);
    const warning = 'Not encrypted: the tunnel token';
    const udpNote = 'The UDP tunnel on port 3081 is not encrypted';
    expect(document.body.textContent).toContain(warning);
    for (const plain of ['tcpmux', 'ws', 'wsmux', 'udpovertcp']) {
      chooseSelectOption('transport', plain);
      await act(async () => {});
      expect(document.body.textContent, plain).toContain(warning);
    }
    // Backhaul's wss carries UDP on a udp tunnel beside it, which the page names.
    chooseSelectOption('transport', 'wssmux — encrypted');
    await act(async () => {});
    expect(document.body.textContent).not.toContain(warning);
    expect(document.body.textContent).toContain(udpNote);

    fireEvent.click(screen.getByText('BackPack'));
    await act(async () => {});
    expect(transportOptions()).toEqual([
      'stealth — encrypted',
      'tcp',
      'tcpmux',
      'ws',
      'wsmux',
      'wss — encrypted',
      'wssmux — encrypted',
      'kcp — encrypted',
      'quic — encrypted',
    ]);
    // BackPack carries UDP on the one tunnel, so stealth leaves nothing readable.
    expect(document.body.textContent).not.toContain(warning);
    expect(document.body.textContent).not.toContain('The UDP tunnel on port');
    for (const plain of ['tcp', 'tcpmux', 'ws', 'wsmux']) {
      chooseSelectOption('transport', plain);
      await act(async () => {});
      expect(document.body.textContent, plain).toContain(warning);
    }
    chooseSelectOption('transport', 'wss — encrypted');
    await act(async () => {});
    expect(document.body.textContent).not.toContain(warning);
    expect(document.body.textContent).not.toContain('The UDP tunnel on port');
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
    expect(document.body.textContent).not.toContain('IPv6 address');
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    expect(document.body.textContent).toContain('Tunnel port');
    expect(document.body.textContent).toContain('Tunnel transport');
    expect(document.body.textContent).toContain('Customers connect to this server’s address');
    cleanup();
  });

  // A new entry server hands customers IPv4 as every front did before, and the
  // operator is told the IPv6 address is found on its own, not to go and look.
  it('starts a new entry server on IPv4 and leaves its IPv6 address to the panel', async () => {
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
    expect(document.body.textContent).toContain('Customers connect over');
    expect(document.body.textContent).toContain('Found on the server automatically after setup');
    expect(familyRadio('').checked).toBe(true);
    expect(familyRadio('v6').checked).toBe(false);
    expect((document.querySelector('#address6') as HTMLInputElement).value).toBe('');
    cleanup();
  });

  // The public name was once dropped by an edit; the IPv6 address and the
  // family have to come back into the form and go out again with the change.
  it('keeps an entry server’s IPv6 address and family through an edit', async () => {
    rows = [{ ...front, address6: '2001:db8::5', ipFamily: 'both' }];
    updateMut.mutateAsync.mockClear();
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(
      document.querySelector('[aria-label="edit"]')?.closest('button') as HTMLElement,
    );
    await act(async () => {});
    expect((document.querySelector('#address6') as HTMLInputElement).value).toBe('2001:db8::5');
    expect(familyRadio('both').checked).toBe(true);
    fireEvent.click(familyRadio('v6'));
    await act(async () => {});
    fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary') as HTMLElement);
    await waitFor(() =>
      expect(updateMut.mutateAsync).toHaveBeenCalledWith({
        id: 3,
        payload: expect.objectContaining({ address6: '2001:db8::5', ipFamily: 'v6' }),
      }),
    );
    cleanup();
  });

  // The IPv6 name turns a changed IPv6 into a DNS edit; an edit that dropped it
  // would put every customer back on the bare literal.
  it('keeps an entry server’s IPv6 domain through an edit', async () => {
    rows = [{ ...front, address6: '2001:db8::5', ipFamily: 'both', publicName6: 'v6.example.ir' }];
    updateMut.mutateAsync.mockClear();
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(
      document.querySelector('[aria-label="edit"]')?.closest('button') as HTMLElement,
    );
    await act(async () => {});
    const name6 = document.querySelector('#publicName6') as HTMLInputElement;
    expect(name6.value).toBe('v6.example.ir');
    fireEvent.change(name6, { target: { value: 'v6b.example.ir' } });
    await act(async () => {});
    fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary') as HTMLElement);
    await waitFor(() =>
      expect(updateMut.mutateAsync).toHaveBeenCalledWith({
        id: 3,
        payload: expect.objectContaining({
          publicName: 'tun.example.ir',
          publicName6: 'v6b.example.ir',
          address6: '2001:db8::5',
        }),
      }),
    );
    cleanup();
  });

  // The IPv6 name sits under the IPv6 address it points at, apart from the
  // IPv4 name, and says it wants an AAAA record only.
  it('asks for the IPv6 domain right after the IPv6 address, only for an entry server', async () => {
    rows = relays;
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText('Add a server'));
    await act(async () => {});
    expect(document.querySelector('#publicName6')).toBeNull();
    fireEvent.click(screen.getByText('Entry for customers'));
    await act(async () => {});
    const labels = fieldLabels();
    expect(labels[labels.indexOf('IPv6 address') + 1]).toBe('IPv6 domain');
    expect(document.body.textContent).toContain('only an AAAA record');
    cleanup();
  });

  // Choosing IPv6 before the panel has the address changes nothing for
  // customers yet, and the form says so rather than letting it look done.
  it('says customers stay on IPv4 until the panel has the IPv6 address', async () => {
    rows = [front];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    fireEvent.click(
      document.querySelector('[aria-label="edit"]')?.closest('button') as HTMLElement,
    );
    await act(async () => {});
    const notice = 'Until the panel has this server’s IPv6 address, customers keep getting IPv4.';
    expect(document.body.textContent).not.toContain(notice);
    fireEvent.click(familyRadio('both'));
    await act(async () => {});
    expect(document.body.textContent).toContain(notice);
    fireEvent.change(document.querySelector('#address6') as HTMLInputElement, {
      target: { value: '2001:db8::5' },
    });
    await act(async () => {});
    expect(document.body.textContent).not.toContain(notice);
    cleanup();
  });

  // The row is where the operator learns whether the IPv6 address customers
  // are handed is one the panel could reach, and what customers get.
  it('shows each entry server’s IPv6 address and whether the panel reached it', () => {
    rows = [
      { ...front, address6: '2001:db8::5', ipFamily: 'both' },
      {
        ...front,
        id: 4,
        name: 'iran-2',
        label: 'Iran 2',
        address6: '2001:db8::6',
        ipFamily: 'v6',
        address6Error: 'dial tcp [2001:db8::6]:443: i/o timeout',
      },
      {
        ...front,
        id: 5,
        name: 'iran-3',
        label: 'Iran 3',
        status: 'working',
        address6: '2001:db8::7',
      },
      { ...front, id: 6, name: 'iran-4', label: 'Iran 4', ipFamily: 'v6' },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = (id: number) => document.querySelector(`tr[data-row-key="${id}"]`) as HTMLElement;
    const reachedMark = (id: number) => row(id).querySelector('.anticon-check-circle');

    expect(row(3).textContent).toContain('IPv6 2001:db8::5');
    expect(reachedMark(3)).not.toBeNull();
    expect(row(3).textContent).toContain('Customers get IPv6 and IPv4');

    expect(row(4).textContent).toContain(
      'The panel could not reach this IPv6 address: dial tcp [2001:db8::6]:443: i/o timeout',
    );
    expect(reachedMark(4)).toBeNull();
    expect(row(4).querySelector('[aria-label="warning"]')).not.toBeNull();
    expect(row(4).textContent).toContain('Customers get IPv6');
    expect(row(4).textContent).not.toContain('Customers get IPv6 and IPv4');

    // Not checked yet while it is still being set up, so no mark either way.
    expect(row(5).textContent).toContain('IPv6 2001:db8::7');
    expect(reachedMark(5)).toBeNull();
    expect(row(5).querySelector('[aria-label="warning"]')).toBeNull();
    expect(row(5).textContent).not.toContain('Customers get');

    expect(row(6).textContent).toContain(
      'Until the panel has this server’s IPv6 address, customers keep getting IPv4.',
    );
    expect(row(6).textContent).not.toContain('Customers get');
    cleanup();
  });

  // The row is where the operator checks which name IPv6 customers are handed.
  it('shows an entry server’s IPv6 domain beside its IPv6 address', () => {
    rows = [{ ...front, address6: '2001:db8::5', ipFamily: 'both', publicName6: 'v6.example.ir' }];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = document.querySelector('tr[data-row-key="3"]') as HTMLElement;
    expect(row.textContent).toContain('203.0.113.9 · tun.example.ir');
    expect(row.textContent).toContain('IPv6 2001:db8::5 · v6.example.ir');
    cleanup();
  });

  // The hint says to leave the IPv6 address blank for setup to read, so a saved
  // domain must show before the address is known, or if reading it fails.
  it('shows a saved IPv6 domain while the IPv6 address is still unknown', () => {
    rows = [
      { ...front, publicName6: 'v6.example.ir' },
      { ...front, id: 4, name: 'iran-2', ipFamily: 'both', publicName6: 'v6b.example.ir' },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = (id: number) => document.querySelector(`tr[data-row-key="${id}"]`) as HTMLElement;
    expect(row(3).textContent).toContain('IPv6 v6.example.ir');
    expect(row(4).textContent).toContain('IPv6 v6b.example.ir');
    expect(row(4).textContent).toContain(
      'Until the panel has this server’s IPv6 address, customers keep getting IPv4.',
    );
    cleanup();
  });

  // A DNS record that does not fit is fixed at the DNS provider; labelled
  // "could not reach", it sent the operator after an address that answers.
  it('tells a problem with the IPv6 domain apart from an IPv6 the panel could not reach', () => {
    rows = [
      {
        ...front,
        address6: '2001:db8::5',
        ipFamily: 'both',
        publicName6: 'v6.example.ir',
        address6Error: 'v6.example.ir also has an A record, so some clients will use IPv4',
      },
      {
        ...front,
        id: 4,
        name: 'iran-2',
        address6: '2001:db8::6',
        ipFamily: 'both',
        publicName6: 'v6.example.ir',
        address6Error: 'dial tcp [2001:db8::6]:443: i/o timeout; v6.example.ir has no AAAA record',
      },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = (id: number) => document.querySelector(`tr[data-row-key="${id}"]`) as HTMLElement;
    expect(row(3).textContent).toContain(
      'Check this server’s IPv6 domain: v6.example.ir also has an A record',
    );
    expect(row(3).textContent).not.toContain('could not reach');
    expect(row(3).querySelector('[aria-label="warning"]')).not.toBeNull();
    // The backend puts a failed dial first; that is still an address that did not answer.
    expect(row(4).textContent).toContain(
      'The panel could not reach this IPv6 address: dial tcp [2001:db8::6]:443',
    );
    expect(row(4).textContent).not.toContain('Check this server’s IPv6 domain');
    cleanup();
  });

  // The backend writes "not checked yet" until its dial ends; read as a failure,
  // every typed address showed "could not reach" before the panel had even tried.
  it('marks an IPv6 address the panel has not dialled yet neither reached nor failed', () => {
    rows = [
      { ...front, address6: '2001:db8::8', ipFamily: 'v6', address6Error: 'not checked yet' },
    ];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = document.querySelector('tr[data-row-key="3"]') as HTMLElement;
    expect(row.textContent).toContain('IPv6 2001:db8::8');
    expect(row.textContent).not.toContain('The panel could not reach this IPv6 address');
    expect(row.querySelector('[aria-label="warning"]')).toBeNull();
    expect(row.querySelector('.anticon-check-circle')).toBeNull();
    cleanup();
  });

  // On a phone the table spilled past its card, the page grew to 735 px and the
  // browser zoomed out to fit, leaving the cards in the left half of the screen.
  it('scrolls the server table sideways inside its card', () => {
    rows = [...relays, front];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const table = document.querySelector('.ant-table') as HTMLElement;
    expect(table.classList.contains('ant-table-scroll-horizontal')).toBe(true);
    cleanup();
  });

  // When the panel found no IPv6 on the server it says why; the row is the only
  // place that reason can reach the operator who picked IPv6.
  it('tells why an entry server set to IPv6 still has no IPv6 address', async () => {
    rows = [{ ...front, ipFamily: 'v6', address6Error: 'the server has no public IPv6 address' }];
    renderWithProviders(
      <MemoryRouter>
        <RelaysPage />
      </MemoryRouter>,
    );
    const row = document.querySelector('tr[data-row-key="3"]') as HTMLElement;
    const notice = within(row).getByText(
      'Until the panel has this server’s IPv6 address, customers keep getting IPv4.',
    );
    fireEvent.mouseEnter(notice);
    await waitFor(() =>
      expect(document.body.textContent).toContain('the server has no public IPv6 address'),
    );
    cleanup();
  });
});

const psiphonCountry = (code: string, name: string, flag: string) => ({
  code,
  name,
  flag,
  enabled: true,
  running: true,
  connected: false,
  region: '',
  exitIp: '',
  exitLoc: '',
  lastError: '',
  socksPort: 0,
  trafficDown: 0,
  trafficUp: 0,
  test: null,
  usedBy: [] as PsiphonCountryUse[],
});

const noScan = {
  running: false,
  stopped: false,
  startedAt: 0,
  finishedAt: 0,
  total: 0,
  results: [],
};

const exitTest = (code: string, fields: Partial<PsiphonExitTest> = {}): PsiphonExitTest => ({
  code,
  connected: false,
  exitIp: '',
  exitLoc: '',
  latencyMs: 0,
  speed: 0,
  error: '',
  at: 1793450000,
  ...fields,
});

// Measured on the live box: DE answered through Psiphon from 164.92.130.120.
const psiphonReady: PsiphonState = {
  supported: true,
  installed: true,
  regions: ['AT', 'DE', 'GB', 'US'],
  regionsAt: 0,
  countries: [
    {
      ...psiphonCountry('DE', 'Germany', '🇩🇪'),
      connected: true,
      region: 'DE',
      exitIp: '164.92.130.120',
      exitLoc: 'DE',
      socksPort: 11181,
    },
    psiphonCountry('US', 'United States', '🇺🇸'),
    {
      ...psiphonCountry('GB', 'United Kingdom', '🇬🇧'),
      running: false,
      lastError: 'psiphon GB exited: no tunnel within 120 s',
    },
  ],
  warp: { mode: 'off', sites: '', registered: false },
  scan: noScan,
};

function renderPage() {
  renderWithProviders(
    <MemoryRouter>
      <RelaysPage />
    </MemoryRouter>,
  );
  return screen.getByText('Psiphon country exits').closest('.ant-card') as HTMLElement;
}

// A country's box in the countries list, by its tick rather than its name, which the
// test results table below repeats.
function listedCountry(card: HTMLElement, code: string): HTMLElement {
  const box = card.querySelector(`input[type="checkbox"][value="${code}"]`) as HTMLElement;
  return box.closest('.ant-col') as HTMLElement;
}

// A row of the test results table under the test button, apart from the scan's own table.
function testResultRow(card: HTMLElement, code: string): HTMLElement | null {
  const header = within(card).getByRole('columnheader', { name: 'Tested' });
  const table = header.closest('.ant-table') as HTMLElement;
  return table.querySelector(`tr[data-row-key="${code}"]`);
}

function fieldIdOf(label: string): string {
  return screen.getByText(label).closest('label')?.getAttribute('for') ?? '';
}

describe('the Psiphon card', () => {
  beforeEach(() => {
    rows = relays;
    psiphonState = psiphonReady;
    psiphonInbounds = [];
  });

  // Before the binary is on the server there is nothing to pick, only the one
  // button, and the operator is told what each country will cost in memory.
  it('offers the install with its memory cost before Psiphon is installed', () => {
    psiphonState = { ...psiphonReady, installed: false, countries: [] };
    installMut.mutate.mockClear();
    const card = renderPage();
    expect(card.textContent).toContain('each country you enable takes about 36 MB of memory');
    expect(card.textContent).not.toContain('Countries per inbound');
    fireEvent.click(within(card).getByText('Install Psiphon').closest('button') as HTMLElement);
    expect(installMut.mutate).toHaveBeenCalledTimes(1);
    cleanup();
  });

  // A restore or a promoted copy has the countries on and no client yet: the card says they
  // are down instead of looking as if Psiphon was never set up, and still offers the install.
  it('says which countries are down while the client is not on the server', () => {
    psiphonState = {
      ...psiphonReady,
      installed: false,
      countries: [
        { ...psiphonReady.countries[0], running: false, connected: false },
        { ...psiphonCountry('AT', 'Austria', '🇦🇹'), enabled: false, running: false },
        { ...psiphonReady.countries[1], running: false },
      ],
    };
    const card = renderPage();
    const alert = card.querySelector('.ant-alert-warning') as HTMLElement;
    expect(alert.textContent).toContain(
      'Switched on: 🇩🇪 Germany, 🇺🇸 United States. The Psiphon client is not on this server yet',
    );
    expect(alert.textContent).not.toContain('Austria');
    expect(within(card).getByText('Install Psiphon')).toBeTruthy();
    cleanup();
  });

  // Psiphon ships x86_64 only; an ARM panel must say so instead of offering a
  // download that would fetch a binary it cannot run.
  it('says why it cannot run on a server Psiphon has no build for', () => {
    psiphonState = { ...psiphonReady, supported: false, installed: false, countries: [] };
    const card = renderPage();
    expect(card.textContent).toContain('Psiphon needs an x86_64 (amd64) Linux server');
    expect(within(card).queryByText('Install Psiphon')).toBeNull();
    cleanup();
  });

  // The tick shows what the operator wants; the tag beside it what is running,
  // and a connected country says where its traffic really comes out.
  it('shows each country as off, connecting, connected with its exit, or failed with why', () => {
    const card = renderPage();
    const cell = (label: string) =>
      within(card).getByText(label).closest('.ant-col') as HTMLElement;
    expect(cell('🇦🇹 Austria').textContent).toContain('Off');
    expect(cell('🇩🇪 Germany').textContent).toContain('Connected');
    expect(cell('🇩🇪 Germany').textContent).toContain('164.92.130.120 · DE');
    expect(cell('🇺🇸 United States').textContent).toContain('Connecting');
    expect(cell('🇬🇧 United Kingdom').textContent).toContain('Failed');
    expect(cell('🇬🇧 United Kingdom').textContent).toContain('psiphon GB exited');
    expect(card.textContent).toContain('1 of 3 connected');
    cleanup();
  });

  // Ticking a country and saving sends the whole set the operator now wants,
  // the ones already running included, or saving would switch those off.
  it('saves the ticked countries together with the ones already running', async () => {
    saveCountriesMut.mutateAsync.mockClear();
    const card = renderPage();
    const save = within(card).getByText('Save countries').closest('button') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    const austria = within(card).getByText('🇦🇹 Austria').closest('label') as HTMLElement;
    fireEvent.click(austria.querySelector('input') as HTMLInputElement);
    await act(async () => {});
    expect(card.textContent).toContain('About 144 MB of memory for 4 countries');
    fireEvent.click(save);
    await waitFor(() => expect(saveCountriesMut.mutateAsync).toHaveBeenCalledTimes(1));
    const sent = saveCountriesMut.mutateAsync.mock.calls[0][0] as string[];
    expect([...sent].sort()).toEqual(['AT', 'DE', 'GB', 'US']);
    cleanup();
  });

  // WireGuard cannot tell countries apart inside one inbound, so its row takes
  // one; a VLESS row takes several, each a config of its own in the subscription.
  it('gives a WireGuard inbound one country and saves what was picked for it', async () => {
    psiphonInbounds = [
      { inboundId: 11, remark: 'vless-ws', protocol: 'vless', countries: ['DE'] },
      { inboundId: 12, remark: 'wg-home', protocol: 'wireguard', countries: [] },
    ];
    saveInboundMut.mutateAsync.mockClear();
    const card = renderPage();
    const row = (id: number) => card.querySelector(`tr[data-row-key="${id}"]`) as HTMLElement;
    expect(row(11).querySelector('.ant-select-multiple')).not.toBeNull();
    expect(row(11).textContent).toContain('🇩🇪 Germany');
    expect(row(12).querySelector('.ant-select-multiple')).toBeNull();
    const select = row(12).querySelector('.ant-select') as HTMLElement;
    fireEvent.mouseDown((select.querySelector('.ant-select-selector') ?? select) as HTMLElement);
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
      (o) => (o.getAttribute('title') ?? o.textContent ?? '').trim() === '🇺🇸 United States',
    );
    fireEvent.click(option as HTMLElement);
    await act(async () => {});
    fireEvent.click(within(row(12)).getByText('Save').closest('button') as HTMLElement);
    await waitFor(() =>
      expect(saveInboundMut.mutateAsync).toHaveBeenCalledWith({ inboundId: 12, codes: ['US'] }),
    );
    cleanup();
  });

  // A line Xray cannot read as a domain rule would only fail when Xray
  // restarts; the form stops it where the operator typed it.
  it('refuses a WARP site line that is not a domain rule', async () => {
    saveWarpMut.mutateAsync.mockClear();
    const card = renderPage();
    chooseSelectOption(fieldIdOf('Send through WARP'), 'Only the sites listed');
    await act(async () => {});
    const sites = document.getElementById(fieldIdOf('Sites')) as HTMLTextAreaElement;
    fireEvent.change(sites, { target: { value: 'gemini.google.com\nnot a rule' } });
    await act(async () => {});
    const save = within(card).getByText('Save WARP').closest('button') as HTMLElement;
    fireEvent.click(save);
    await waitFor(() => expect(card.textContent).toContain('Each line must be a domain'));
    expect(saveWarpMut.mutateAsync).not.toHaveBeenCalled();
    fireEvent.change(sites, { target: { value: 'gemini.google.com\nkeyword:openai' } });
    await act(async () => {});
    fireEvent.click(save);
    await waitFor(() =>
      expect(saveWarpMut.mutateAsync).toHaveBeenCalledWith({
        mode: 'sites',
        sites: 'gemini.google.com\nkeyword:openai',
      }),
    );
    cleanup();
  });

  // What customers moved through each country, one switched off included, and
  // in all; the reset clears every country, so it waits for a confirm.
  it('shows each country’s traffic and the total, and resets only once confirmed', async () => {
    psiphonState = {
      ...psiphonReady,
      countries: [
        { ...psiphonReady.countries[0], trafficDown: 699355553, trafficUp: 75264133 },
        psiphonReady.countries[1],
        { ...psiphonReady.countries[2], trafficDown: 1073741824 },
        {
          ...psiphonCountry('AT', 'Austria', '🇦🇹'),
          enabled: false,
          running: false,
          trafficDown: 536870912,
        },
      ],
    };
    resetTrafficMut.mutateAsync.mockClear();
    const card = renderPage();
    const cell = (label: string) =>
      within(card).getByText(label).closest('.ant-col') as HTMLElement;
    expect(cell('🇩🇪 Germany').textContent).toContain('↓ 667 MB · ↑ 72 MB');
    expect(cell('🇬🇧 United Kingdom').textContent).toContain('↓ 1.0 GB · ↑ 0');
    expect(cell('🇦🇹 Austria').textContent).toContain('Off');
    expect(cell('🇦🇹 Austria').textContent).toContain('↓ 512 MB · ↑ 0');
    expect(cell('🇺🇸 United States').textContent).not.toContain('↓');
    expect(card.textContent).toContain('Through Psiphon: ↓ 2.2 GB · ↑ 72 MB');

    fireEvent.click(within(card).getByText('Reset traffic').closest('button') as HTMLElement);
    await waitFor(() =>
      expect(document.body.textContent).toContain('Reset the traffic of every country?'),
    );
    expect(resetTrafficMut.mutateAsync).not.toHaveBeenCalled();
    const confirm = document.querySelector('.ant-popconfirm') as HTMLElement;
    fireEvent.click(within(confirm).getByText('Reset').closest('button') as HTMLElement);
    await waitFor(() => expect(resetTrafficMut.mutateAsync).toHaveBeenCalledWith({ all: true }));
    cleanup();
  });

  // A country connects within seconds, so the card asks often only while one
  // is still on its way or a scan reports; a failed one waits for the slow round.
  it('polls quickly only while a country is still connecting or a scan runs', () => {
    expect(psiphonRefetchMs(psiphonReady)).toBe(5000);
    const settled = {
      ...psiphonReady,
      countries: psiphonReady.countries.filter((c) => c.code !== 'US'),
    };
    expect(psiphonRefetchMs(settled)).toBe(30000);
    expect(psiphonRefetchMs({ ...settled, scan: { ...noScan, running: true, total: 27 } })).toBe(
      3000,
    );
  });

  // The test reads the running countries through their own clients; its table beside the
  // scan shows each one's exit, round trip and download speed, or why the test failed.
  it('tests the running countries and shows each one’s speed or failure', () => {
    testMut.mutate.mockClear();
    psiphonState = {
      ...psiphonReady,
      countries: [
        {
          ...psiphonReady.countries[0],
          test: exitTest('DE', {
            connected: true,
            exitIp: '164.92.130.120',
            exitLoc: 'DE',
            latencyMs: 412,
            speed: 1258291,
          }),
        },
        {
          ...psiphonReady.countries[1],
          test: exitTest('US', { error: 'socks connect tcp 127.0.0.1:11182: connection refused' }),
        },
        psiphonReady.countries[2],
      ],
    };
    const card = renderPage();
    const row = (code: string) => testResultRow(card, code);
    expect(row('DE')?.textContent).toContain('🇩🇪 Germany');
    expect(row('DE')?.textContent).toContain('164.92.130.120 · DE');
    expect(row('DE')?.textContent).toContain('412 ms');
    expect(row('DE')?.textContent).toContain('1.2 MB/s');
    expect(row('US')?.textContent).toContain('socks connect tcp 127.0.0.1:11182');
    expect(row('GB')).toBeNull();
    // The countries list above keeps only the tick, the state and the traffic.
    expect(listedCountry(card, 'DE').textContent).toContain('🇩🇪 Germany');
    expect(listedCountry(card, 'DE').textContent).not.toContain('412 ms');
    expect(listedCountry(card, 'DE').textContent).not.toContain('Tested');
    expect(listedCountry(card, 'US').textContent).not.toContain('socks connect');
    fireEvent.click(
      within(card).getByText('Test enabled countries').closest('button') as HTMLElement,
    );
    expect(testMut.mutate).toHaveBeenCalledTimes(1);
    cleanup();

    // With no country running there is nothing to test through.
    psiphonState = {
      ...psiphonReady,
      countries: psiphonReady.countries.map((c) => ({ ...c, running: false })),
    };
    const idle = renderPage();
    const button = within(idle)
      .getByText('Test enabled countries')
      .closest('button') as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    cleanup();
  });

  // A scan reports each country as it lands, and the button waits until it is done
  // rather than sending a second scan the panel would refuse.
  it('follows a running scan country by country', () => {
    scanMut.mutate.mockClear();
    psiphonState = {
      ...psiphonReady,
      scan: {
        ...noScan,
        running: true,
        startedAt: 1793450000,
        total: 27,
        results: [
          exitTest('FI', {
            connected: true,
            exitIp: '65.21.0.10',
            exitLoc: 'FI',
            latencyMs: 380,
            speed: 2097152,
          }),
          exitTest('IT'),
        ],
      },
    };
    const card = renderPage();
    expect(card.textContent).toContain('Scanning: 2 of 27 countries tried');
    const scan = within(card)
      .getByText('Scan all countries')
      .closest('button') as HTMLButtonElement;
    expect(scan.disabled).toBe(true);
    const row = (code: string) => card.querySelector(`tr[data-row-key="${code}"]`) as HTMLElement;
    expect(row('FI').textContent).toContain('Works');
    expect(row('FI').textContent).toContain('65.21.0.10 · FI');
    expect(row('FI').textContent).toContain('380 ms');
    expect(row('FI').textContent).toContain('2.0 MB/s');
    expect(row('IT').textContent).toContain('Did not connect');
    expect(row('IT').textContent).not.toContain('Enable');
    cleanup();

    psiphonState = { ...psiphonReady, scan: noScan };
    const fresh = renderPage();
    fireEvent.click(within(fresh).getByText('Scan all countries').closest('button') as HTMLElement);
    expect(scanMut.mutate).toHaveBeenCalledTimes(1);
    cleanup();
  });

  // A finished scan says how many countries work; one that works and is not on yet is
  // switched on from its row, together with the ones already running.
  it('switches on a country the last scan found working', async () => {
    saveCountriesMut.mutateAsync.mockClear();
    psiphonState = {
      ...psiphonReady,
      scan: {
        ...noScan,
        startedAt: 1793450000,
        finishedAt: 1793450600,
        total: 3,
        results: [
          exitTest('FI', {
            connected: true,
            exitIp: '65.21.0.10',
            exitLoc: 'FI',
            latencyMs: 380,
            speed: 2097152,
          }),
          exitTest('DE', {
            connected: true,
            exitIp: '164.92.130.120',
            exitLoc: 'DE',
            latencyMs: 300,
            speed: 1048576,
          }),
          exitTest('PT', {
            connected: true,
            exitIp: '198.51.100.4',
            error: 'the download through Psiphon answered 403 Forbidden',
          }),
        ],
      },
    };
    const card = renderPage();
    expect(card.textContent).toMatch(/Last scan .+: 2 of 3 countries work/);
    const row = (code: string) => card.querySelector(`tr[data-row-key="${code}"]`) as HTMLElement;
    expect(row('DE').textContent).toContain('Enabled');
    expect(row('PT').textContent).toContain('Connected, test failed');
    expect(within(row('PT')).queryByText('Enable')).toBeNull();
    fireEvent.click(within(row('FI')).getByText('Enable').closest('button') as HTMLElement);
    await waitFor(() => expect(saveCountriesMut.mutateAsync).toHaveBeenCalledTimes(1));
    const sent = saveCountriesMut.mutateAsync.mock.calls[0][0] as string[];
    expect([...sent].sort()).toEqual(['DE', 'FI', 'GB', 'US']);
    cleanup();
  });

  // A restart or standby stops a scan midway; the card says so instead of passing the
  // countries it never reached off as tried.
  it('says when the last scan was cut short', () => {
    psiphonState = {
      ...psiphonReady,
      scan: {
        ...noScan,
        stopped: true,
        startedAt: 1793450000,
        finishedAt: 1793450100,
        total: 27,
        results: [exitTest('AT')],
      },
    };
    const card = renderPage();
    expect(card.textContent).toContain(
      'The last scan was cut short by a restart or standby after 1 of 27 countries',
    );
    cleanup();
  });

  // The panel refuses to switch off a country customers still leave through; the card
  // locks it and says exactly where, and names one that is off yet still in use.
  it('locks a country in use and says where it is used', async () => {
    psiphonState = {
      ...psiphonReady,
      countries: [
        {
          ...psiphonReady.countries[0],
          usedBy: [
            { kind: 'host', inbound: 'in-2576-udp', count: 2, location: '' },
            { kind: 'cisco', inbound: 'in-443-tcp', count: 1, location: 'austria' },
            { kind: 'openvpn', inbound: 'in-1194-tcp', count: 1, location: '' },
          ],
        },
        {
          ...psiphonReady.countries[1],
          usedBy: [{ kind: 'host', inbound: 'in-8443-tcp', count: 1, location: '' }],
        },
        {
          ...psiphonReady.countries[2],
          enabled: false,
          usedBy: [{ kind: 'cisco', inbound: 'in-444-tcp', count: 1, location: '' }],
        },
      ],
    };
    const card = renderPage();
    const cell = (label: string) =>
      within(card).getByText(label).closest('.ant-col') as HTMLElement;
    const box = (label: string) => cell(label).querySelector('input') as HTMLInputElement;
    expect(box('🇩🇪 Germany').checked).toBe(true);
    expect(box('🇩🇪 Germany').disabled).toBe(true);
    fireEvent.mouseEnter(within(cell('🇩🇪 Germany')).getByText('In use'));
    await waitFor(() =>
      expect(document.body.textContent).toContain(
        'Customers leave through it: 2 hosts on in-2576-udp · cisco in-443-tcp, location austria · OpenVPN in-1194-tcp. Take it off there before switching it off.',
      ),
    );
    expect(box('🇺🇸 United States').disabled).toBe(true);
    cleanup();

    const again = renderPage();
    const tag = within(
      within(again).getByText('🇺🇸 United States').closest('.ant-col') as HTMLElement,
    ).getByText('In use');
    fireEvent.mouseEnter(tag);
    await waitFor(() => expect(document.body.textContent).toContain('a host on in-8443-tcp.'));
    cleanup();

    const third = renderPage();
    const gb = within(third).getByText('🇬🇧 United Kingdom').closest('.ant-col') as HTMLElement;
    expect((gb.querySelector('input') as HTMLInputElement).disabled).toBe(false);
    expect((gb.querySelector('input') as HTMLInputElement).checked).toBe(false);
    fireEvent.mouseEnter(within(gb).getByText('In use'));
    await waitFor(() =>
      expect(document.body.textContent).toContain(
        'Switched off, yet still the exit of: cisco in-444-tcp.',
      ),
    );
    const austria = within(third).getByText('🇦🇹 Austria').closest('.ant-col') as HTMLElement;
    expect(within(austria).queryByText('In use')).toBeNull();
    cleanup();
  });

  // The test takes up to 30 s and nothing showed it running; a result then said when it
  // ran only in a tooltip. Now each running country spins, and the age shows and moves.
  it('shows each country testing, then how long ago and what changed', async () => {
    vi.useFakeTimers({
      toFake: ['Date', 'setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'],
    });
    const now = Math.floor(Date.now() / 1000);
    const measured = { connected: true, exitIp: '164.92.130.120', exitLoc: 'DE' };
    const tested = (de: Partial<PsiphonExitTest>, us: Partial<PsiphonExitTest>): PsiphonState => ({
      ...psiphonReady,
      countries: [
        { ...psiphonReady.countries[0], test: exitTest('DE', { ...measured, ...de }) },
        { ...psiphonReady.countries[1], test: exitTest('US', { ...measured, ...us }) },
        psiphonReady.countries[2],
        { ...psiphonCountry('AT', 'Austria', '🇦🇹'), enabled: false, running: false },
      ],
    });
    const before = { latencyMs: 412, speed: 1258291, at: now - 125 };
    psiphonState = tested(before, before);
    const poll = { again: () => {} };
    function Polled() {
      const [, setTick] = useState(0);
      useEffect(() => {
        poll.again = () => setTick((n) => n + 1);
      }, []);
      return (
        <MemoryRouter>
          <RelaysPage />
        </MemoryRouter>
      );
    }
    renderWithProviders(<Polled />);
    const card = screen.getByText('Psiphon country exits').closest('.ant-card') as HTMLElement;
    const row = (code: string) => testResultRow(card, code);
    try {
      expect(row('US')?.textContent).toContain('2 minutes ago');
      testMut.isPending = true;
      act(() => poll.again());
      expect(row('DE')?.textContent).toContain('Testing…');
      expect(row('US')?.textContent).toContain('Testing…');
      // Only the enabled countries with a running client are tested.
      expect(row('GB')).toBeNull();
      expect(row('AT')).toBeNull();
      expect(listedCountry(card, 'DE').textContent).not.toContain('Testing…');

      testMut.isPending = false;
      psiphonState = tested({ latencyMs: 300, speed: 2097152, at: now }, { ...before, at: now });
      act(() => poll.again());
      expect(row('DE')?.textContent).not.toContain('Testing…');
      expect(row('DE')?.textContent).toContain('300 ms');
      expect(row('DE')?.textContent).toContain('just now');
      expect(row('US')?.textContent).toContain('just now');
      expect(listedCountry(card, 'DE').textContent).not.toContain('just now');
      // Germany's numbers moved; the United States measured the same again.
      expect(row('DE')?.querySelector('.psiphon-test-changed')).not.toBeNull();
      expect(row('US')?.querySelector('.psiphon-test-changed')).toBeNull();
      expect(listedCountry(card, 'DE').querySelector('.psiphon-test-changed')).toBeNull();

      await act(async () => {
        vi.advanceTimersByTime(3 * 60_000);
      });
      expect(row('DE')?.textContent).toContain('3 minutes ago');
      expect(row('DE')?.querySelector('.psiphon-test-changed')).toBeNull();
    } finally {
      testMut.isPending = false;
      vi.useRealTimers();
      cleanup();
    }
  });
});

function familyRadio(value: string): HTMLInputElement {
  return document.querySelector(`input[type="radio"][value="${value}"]`) as HTMLInputElement;
}
