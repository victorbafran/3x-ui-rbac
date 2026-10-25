import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';

import type { InboundClientLinks, TunnelConfig } from '@/generated/types';
import { ThemeProvider } from '@/hooks/useTheme';
import InboundInfoModal from '@/pages/inbounds/info/InboundInfoModal';
import type { DBInboundLike } from '@/pages/inbounds/info/types';
import { HttpUtil, Msg } from '@/utils';
import { appCacheQueryClient, resellerQueryClient, renderWithProviders } from './test-utils';

const INBOUND_ID = 7;
const setupGet = vi.mocked(HttpUtil.get).getMockImplementation()!;

// Only this inbound's URL answers, so a request for any other id renders nothing.
function serveInboundLinks(entries: InboundClientLinks[]) {
  vi.mocked(HttpUtil.get).mockImplementation(async (url: string, ...rest) => {
    if (url === `/panel/api/inbounds/${INBOUND_ID}/links`) return new Msg(true, '', entries);
    return setupGet(url, ...rest);
  });
}

afterEach(() => {
  vi.mocked(HttpUtil.get).mockImplementation(setupGet);
});

function tunnelConfig(email: string, family: string, text: string): TunnelConfig {
  return {
    inboundId: INBOUND_ID,
    protocol: 'wireguard',
    email,
    address: '',
    port: 51820,
    family,
    endpoint: '',
    remark: '',
    text,
  };
}

function dbInbound(protocol: string, clients: Record<string, unknown>[]): DBInboundLike {
  const settings =
    protocol === 'wireguard'
      ? { secretKey: 'iJ2cBkrSGqRwIfYIDIxk7hr5RXfdR93MfJUL7yqkkH8=', mtu: 1420, peers: [], clients }
      : { clients, decryption: 'none' };
  return {
    id: INBOUND_ID,
    address: '',
    port: 51820,
    listen: '',
    protocol,
    remark: 'unit',
    enable: true,
    isWireguard: protocol === 'wireguard',
    settings,
    streamSettings: { network: 'tcp', security: 'none' },
    sniffing: {},
    clientStats: [],
  };
}

function linkPanels() {
  return Array.from(document.querySelectorAll('.link-panel')).map((panel) => [
    Array.from(panel.querySelectorAll('.link-panel-header .ant-tag')).map((tag) => tag.textContent),
    panel.querySelector('code')?.textContent,
  ]);
}

describe('InboundInfoModal configs from the backend', () => {
  const wgClients = [
    { email: 'alice', enable: true },
    { email: 'bob', enable: true },
  ];

  async function openInboundTab(entries: InboundClientLinks[]) {
    serveInboundLinks(entries);
    renderWithProviders(
      <InboundInfoModal open onClose={() => {}} dbInbound={dbInbound('wireguard', wgClients)} />,
    );
    fireEvent.click(screen.getByRole('tab', { name: 'Inbound' }));
  }

  it('numbers each peer by its place in settings, whatever order the response has', async () => {
    await openInboundTab([
      {
        email: 'bob',
        links: ['wireguard://bob@h:51820#bob'],
        configs: [tunnelConfig('bob', 'v6', 'BOB-6'), tunnelConfig('bob', 'v4', 'BOB-4')],
      },
      {
        email: 'alice',
        links: ['wireguard://alice@h:51820#alice'],
        configs: [tunnelConfig('alice', '', 'ALICE')],
      },
    ]);

    await waitFor(() => expect(linkPanels()).toHaveLength(5));
    expect(linkPanels()).toEqual([
      [['Peer 1 config'], 'ALICE'],
      [['Peer 1 link'], 'wireguard://alice@h:51820#alice'],
      [['Peer 2 config', 'IPv6'], 'BOB-6'],
      [['Peer 2 config', 'IPv4'], 'BOB-4'],
      [['Peer 2 link'], 'wireguard://bob@h:51820#bob'],
    ]);
  });

  it("shows a reseller only the peers the response carries, keeping each one's number", async () => {
    await openInboundTab([{ email: 'bob', links: [], configs: [tunnelConfig('bob', '', 'BOB')] }]);

    await waitFor(() => expect(linkPanels()).toHaveLength(1));
    expect(linkPanels()).toEqual([[['Peer 2 config'], 'BOB']]);
  });
});

describe('InboundInfoModal client links from the backend', () => {
  it("lists the selected client's links, named by their own remark", async () => {
    serveInboundLinks([
      { email: 'alice', links: ['vless://a@h:443#alice'], configs: [] },
      {
        email: 'bob',
        links: [
          'vless://b@[2001:db8::1]:443#bob%20%C2%B7%20IPv6',
          'vless://b@h:443#bob%20%C2%B7%20IPv4',
        ],
        configs: [],
      },
    ]);
    renderWithProviders(
      <InboundInfoModal
        open
        onClose={() => {}}
        clientIndex={1}
        dbInbound={dbInbound('vless', [
          { email: 'alice', id: 'a', enable: true },
          { email: 'bob', id: 'b', enable: true },
        ])}
      />,
    );

    await waitFor(() => expect(linkPanels()).toHaveLength(2));
    expect(linkPanels()).toEqual([
      [['bob · IPv6'], 'vless://b@[2001:db8::1]:443#bob%20%C2%B7%20IPv6'],
      [['bob · IPv4'], 'vless://b@h:443#bob%20%C2%B7%20IPv4'],
    ]);
  });

  const vlessInbound = dbInbound('vless', [{ email: 'alice', id: 'a', enable: true }]);

  // A Host or entry server edited on its own page invalidates nothing under inbounds.
  it('shows an edit made since the window was last open', async () => {
    const queryClient = appCacheQueryClient();
    const info = (open: boolean) => (
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <InboundInfoModal open={open} onClose={() => {}} dbInbound={vlessInbound} />
        </ThemeProvider>
      </QueryClientProvider>
    );
    serveInboundLinks([{ email: 'alice', links: ['vless://a@old:443#alice'], configs: [] }]);
    const { rerender } = render(info(true));
    await waitFor(() => expect(linkPanels()).toEqual([[['alice'], 'vless://a@old:443#alice']]));

    rerender(info(false));
    serveInboundLinks([{ email: 'alice', links: ['vless://a@new:443#alice'], configs: [] }]);
    rerender(info(true));
    await waitFor(() => expect(linkPanels()).toEqual([[['alice'], 'vless://a@new:443#alice']]));
  });

  it('asks for no link the caller may not see', async () => {
    serveInboundLinks([{ email: 'alice', links: ['vless://a@h:443#alice'], configs: [] }]);
    vi.mocked(HttpUtil.get).mockClear();
    renderWithProviders(<InboundInfoModal open onClose={() => {}} dbInbound={vlessInbound} />, {
      queryClient: resellerQueryClient('clients.viewLinks'),
    });

    await screen.findByRole('tab', { name: 'Inbound' });
    expect(vi.mocked(HttpUtil.get).mock.calls.map(([url]) => url)).not.toContain(
      `/panel/api/inbounds/${INBOUND_ID}/links`,
    );
  });
});
