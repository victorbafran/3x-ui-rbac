import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, waitFor, within } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';

import type { TunnelConfig } from '@/generated/types';
import type { ClientRecord, InboundOption } from '@/hooks/useClients';
import { ThemeProvider } from '@/hooks/useTheme';
import ClientInfoModal from '@/pages/clients/ClientInfoModal';
import ClientQrModal from '@/pages/clients/ClientQrModal';
import { FileManager, HttpUtil, Msg } from '@/utils';
import { appCacheQueryClient, renderWithProviders } from './test-utils';

vi.mock('@/pages/inbounds/qr', () => ({
  QrPanel: ({
    value,
    remark,
    downloadName,
  }: {
    value: string;
    remark?: string;
    downloadName?: string;
  }) => (
    <pre data-testid="qr" data-remark={remark ?? ''} data-download={downloadName ?? ''}>
      {value}
    </pre>
  ),
}));

const CLIENT_ID = 42;
const INBOUNDS: Record<number, InboundOption> = {
  201: { id: 201, tag: 'wg-us', remark: 'US · New York', protocol: 'wireguard', port: 51820 },
  202: { id: 202, tag: 'wg-eu', remark: 'EU · Frankfurt', protocol: 'wireguard', port: 51821 },
  101: { id: 101, tag: 'awg-de', remark: 'DE · Kelsterbach', protocol: 'amneziawg', port: 52716 },
  301: { id: 301, tag: 'tuic-a', remark: 'tuic', protocol: 'tuic', port: 8443 },
  302: { id: 302, tag: 'tuic-b', remark: 'tuic-b', protocol: 'tuic', port: 9443 },
};

function client(email: string): ClientRecord {
  return { id: CLIENT_ID, email, subId: 'alpha', inboundIds: [201, 202, 101, 301, 302] };
}

function config(fields: Partial<TunnelConfig> & Pick<TunnelConfig, 'inboundId' | 'protocol'>) {
  return {
    email: 'alice',
    address: 'panel.example.com',
    port: 51820,
    family: '',
    endpoint: '',
    remark: '',
    text: `[Interface] ${fields.inboundId} ${fields.endpoint ?? ''}`,
    ...fields,
  } satisfies TunnelConfig;
}

const setupGet = vi.mocked(HttpUtil.get).getMockImplementation()!;

// Only the client record's own URL answers, so a request for any other id renders nothing.
function serveTunnelConfigs(configs: TunnelConfig[]) {
  vi.mocked(HttpUtil.get).mockImplementation(async (url: string, ...rest) => {
    if (url === `/panel/api/clients/tunnelConfigs/${CLIENT_ID}`) return new Msg(true, '', configs);
    if (url.startsWith('/panel/api/clients/subLinks/')) return new Msg(true, '', []);
    return setupGet(url, ...rest);
  });
}

afterEach(() => {
  vi.mocked(HttpUtil.get).mockImplementation(setupGet);
});

function renderQr(email: string) {
  renderWithProviders(
    <MemoryRouter initialEntries={['/clients']}>
      <ClientQrModal open client={client(email)} inboundsById={INBOUNDS} onOpenChange={() => {}} />
    </MemoryRouter>,
  );
}

// Opens every collapsed panel so each one's QrPanel renders.
async function qrPanels(count: number) {
  await waitFor(() => expect(document.querySelectorAll('.ant-collapse-item')).toHaveLength(count));
  for (const header of document.querySelectorAll<HTMLElement>('.ant-collapse-header')) {
    if (!header.closest('.ant-collapse-item-active')) fireEvent.click(header);
  }
  return Array.from(document.querySelectorAll('.ant-collapse-item')).map((item) => {
    const qr = within(item as HTMLElement).getByTestId('qr');
    return {
      header: item.querySelector('.ant-collapse-header')!.textContent,
      value: qr.textContent,
      remark: qr.getAttribute('data-remark'),
      download: qr.getAttribute('data-download'),
    };
  });
}

describe('ClientQrModal tunnel configs from the backend', () => {
  it('names an IPv4-only config the way the browser-built one was named', async () => {
    serveTunnelConfigs([config({ inboundId: 201, protocol: 'wireguard', text: 'WG-US' })]);
    renderQr('alice');

    expect(await qrPanels(1)).toEqual([
      { header: 'WireGuard config', value: 'WG-US', remark: 'alice', download: 'alice.conf' },
    ]);
  });

  it('labels each inbound and Host when the client has several configs', async () => {
    serveTunnelConfigs([
      config({ inboundId: 201, protocol: 'wireguard', endpoint: 'edge.example.com:443' }),
      config({ inboundId: 202, protocol: 'wireguard' }),
      config({ inboundId: 101, protocol: 'amneziawg', text: 'AWG' }),
    ]);
    renderQr('WG-CLIENT');

    const panels = await qrPanels(3);
    expect(panels.map(({ header, remark, download }) => [header, remark, download])).toEqual([
      [
        'WireGuard configUS · New York - edge.example.com:443',
        'US · New York - edge.example.com:443 - WG-CLIENT',
        'WG-CLIENT-US_New_York-edge.example.com_443.conf',
      ],
      [
        'WireGuard configEU · Frankfurt',
        'EU · Frankfurt - WG-CLIENT',
        'WG-CLIENT-EU_Frankfurt.conf',
      ],
      ['AmneziaWG config', 'WG-CLIENT', 'WG-CLIENT.conf'],
    ]);
  });

  it("tags an entry server's IPv6 and IPv4 configs", async () => {
    serveTunnelConfigs([
      config({
        inboundId: 201,
        protocol: 'wireguard',
        family: 'v6',
        endpoint: '[2001:db8::1]:51820',
        text: '# US · New York - alice · IPv6',
      }),
      config({
        inboundId: 201,
        protocol: 'wireguard',
        family: 'v4',
        endpoint: '203.0.113.5:51820',
        text: '# US · New York - alice · IPv4',
      }),
    ]);
    renderQr('alice');

    expect(await qrPanels(2)).toEqual([
      {
        header: 'WireGuard configIPv6US · New York - [2001:db8::1]:51820',
        value: '# US · New York - alice · IPv6',
        remark: 'US · New York - [2001:db8::1]:51820 - alice',
        download: 'alice-US_New_York-_2001_db8_1_51820.conf',
      },
      {
        header: 'WireGuard configIPv4US · New York - 203.0.113.5:51820',
        value: '# US · New York - alice · IPv4',
        remark: 'US · New York - 203.0.113.5:51820 - alice',
        download: 'alice-US_New_York-203.0.113.5_51820.conf',
      },
    ]);
  });

  it('keeps the TUIC config to the first TUIC inbound, one per Host', async () => {
    serveTunnelConfigs([
      config({ inboundId: 301, protocol: 'tuic', endpoint: 'clash.example.com:443', text: 'A1' }),
      config({ inboundId: 301, protocol: 'tuic', endpoint: 'clash2.example.com:443', text: 'A2' }),
      config({ inboundId: 302, protocol: 'tuic', text: 'B' }),
    ]);
    renderQr('TUIC-CLIENT');

    const panels = await qrPanels(2);
    expect(
      panels.map(({ header, value, remark, download }) => [header, value, remark, download]),
    ).toEqual([
      [
        'TUIC config (Clash)clash.example.com:443',
        'A1',
        'TUIC-CLIENT - clash.example.com:443',
        'TUIC-CLIENT-clash.example.com_443.yaml',
      ],
      [
        'TUIC config (Clash)clash2.example.com:443',
        'A2',
        'TUIC-CLIENT - clash2.example.com:443',
        'TUIC-CLIENT-clash2.example.com_443.yaml',
      ],
    ]);
  });

  it('names a single TUIC config after the client alone', async () => {
    serveTunnelConfigs([config({ inboundId: 301, protocol: 'tuic', text: 'A' })]);
    renderQr('TUIC-CLIENT');

    expect(await qrPanels(1)).toEqual([
      {
        header: 'TUIC config (Clash)',
        value: 'A',
        remark: 'TUIC-CLIENT',
        download: 'TUIC-CLIENT.yaml',
      },
    ]);
  });

  // An inbound, Host or entry server edited elsewhere invalidates nothing under clients.
  it('shows an edit made since the window was last open', async () => {
    const queryClient = appCacheQueryClient();
    const qr = (open: boolean) => (
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <MemoryRouter initialEntries={['/clients']}>
            <ClientQrModal
              open={open}
              client={client('alice')}
              inboundsById={INBOUNDS}
              onOpenChange={() => {}}
            />
          </MemoryRouter>
        </ThemeProvider>
      </QueryClientProvider>
    );
    serveTunnelConfigs([config({ inboundId: 201, protocol: 'wireguard', text: 'Endpoint = old' })]);
    const { rerender } = render(qr(true));
    expect((await qrPanels(1))[0].value).toBe('Endpoint = old');

    rerender(qr(false));
    serveTunnelConfigs([config({ inboundId: 201, protocol: 'wireguard', text: 'Endpoint = new' })]);
    rerender(qr(true));
    await waitFor(async () => expect((await qrPanels(1))[0].value).toBe('Endpoint = new'));
  });
});

describe('ClientInfoModal tunnel configs from the backend', () => {
  function renderInfo(email: string) {
    renderWithProviders(
      <ClientInfoModal
        open
        client={client(email)}
        inboundsById={INBOUNDS}
        isOnline={false}
        onOpenChange={() => {}}
      />,
    );
  }

  async function downloads() {
    const spy = vi.spyOn(FileManager, 'downloadTextFile').mockImplementation(() => {});
    await waitFor(() =>
      expect(document.querySelectorAll('.config-block').length).toBeGreaterThan(0),
    );
    const out = Array.from(document.querySelectorAll('.config-block')).map((block) => {
      fireEvent.click(within(block as HTMLElement).getByRole('button', { name: 'Download' }));
      const [text, fileName] = spy.mock.calls.at(-1)!;
      return [block.querySelector('.ant-collapse-header .ant-tag')!.textContent, text, fileName];
    });
    spy.mockRestore();
    return out;
  }

  it('keeps the IPv4-only label and file name and leaves TUIC out', async () => {
    serveTunnelConfigs([
      config({ inboundId: 201, protocol: 'wireguard', text: 'WG-US' }),
      config({ inboundId: 301, protocol: 'tuic', text: 'TUIC' }),
    ]);
    renderInfo('alice');

    expect(await downloads()).toEqual([['Config', 'WG-US', 'alice.conf']]);
  });

  it("tags an entry server's IPv6 and IPv4 configs", async () => {
    serveTunnelConfigs([
      config({
        inboundId: 101,
        protocol: 'amneziawg',
        family: 'v6',
        endpoint: '[2001:db8::1]:52716',
        text: 'AWG6',
      }),
      config({
        inboundId: 101,
        protocol: 'amneziawg',
        family: 'v4',
        endpoint: '203.0.113.5:52716',
        text: 'AWG4',
      }),
    ]);
    renderInfo('NSK-RT-01');

    expect(await downloads()).toEqual([
      [
        'DE · Kelsterbach - [2001:db8::1]:52716 · IPv6',
        'AWG6',
        'NSK-RT-01-DE_Kelsterbach-_2001_db8_1_52716.conf',
      ],
      [
        'DE · Kelsterbach - 203.0.113.5:52716 · IPv4',
        'AWG4',
        'NSK-RT-01-DE_Kelsterbach-203.0.113.5_52716.conf',
      ],
    ]);
  });
});
