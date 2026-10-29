import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, waitFor, within } from '@testing-library/react';

const { createObjectURL, revokeObjectURL } = URL;

afterEach(() => {
  URL.createObjectURL = createObjectURL;
  URL.revokeObjectURL = revokeObjectURL;
  delete window.__SUB_PAGE_DATA__;
  vi.restoreAllMocks();
});

const wireguard = (remark: string) =>
  `wireguard://cHJpdmF0ZQ@203.0.113.5:51820?publickey=cHVibGlj&address=10.0.0.2%2F32#${encodeURIComponent(remark)}`;

function amneziawg(remark: string) {
  const conf = `[Interface]\nPrivateKey = cHJpdmF0ZQ\nAddress = 10.8.1.2/32\n\n# ${remark}\n[Peer]\nPublicKey = cHVibGlj\nEndpoint = 203.0.113.5:52716\n`;
  let binary = '';
  for (const byte of new TextEncoder().encode(conf)) binary += String.fromCharCode(byte);
  return `vpn://${btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
}

const vless = (remark: string) =>
  `vless://8f3c@203.0.113.5:443?type=tcp&security=reality#${encodeURIComponent(remark)}`;

// The page reads its data once, as it loads, so each render loads it afresh.
async function renderSubPage(data: NonNullable<Window['__SUB_PAGE_DATA__']>) {
  window.__SUB_PAGE_DATA__ = data;
  vi.resetModules();
  const { renderWithProviders } = await import('./test-utils');
  const { default: SubPage } = await import('@/pages/sub/SubPage');
  renderWithProviders(<SubPage />);
}

async function downloadNames() {
  URL.createObjectURL = vi.fn(() => 'blob:download');
  URL.revokeObjectURL = vi.fn();
  const saved: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    saved.push(this.download);
  });
  await waitFor(() => expect(document.querySelectorAll('.config-block').length).toBeGreaterThan(0));
  for (const block of document.querySelectorAll<HTMLElement>('.config-block')) {
    fireEvent.click(within(block).getByRole('button', { name: 'Download' }));
  }
  return saved;
}

describe('subscription page tunnel files', () => {
  // A hosts-first subscription pairs only the first link with the client's email.
  it("name each file after the page's one client and its config's country", async () => {
    await renderSubPage({
      links: [
        vless('🇩🇪 De VLESS'),
        wireguard('🇩🇪 De - Epic-vpnPromax · IPv6'),
        wireguard('🇩🇪 De - Epic-vpnPromax · IPv4'),
        amneziawg('🇫🇷 Fr - Epic-vpnPromax'),
      ],
      emails: ['Epic-vpnPromax', '', '', ''],
    });

    expect(await downloadNames()).toEqual([
      'Epic-vpnPro-DE1.conf',
      'Epic-vpnPro-DE2.conf',
      'Epic-vpnProm-FR.conf',
    ]);
  });

  it('name each file after the one client its remark carries', async () => {
    await renderSubPage({
      links: [wireguard('🇩🇪 De - bob')],
      emails: ['bob'],
      ciscoLogins: [{ email: 'alice', server: 'cisco.example.com', username: 'alice' }],
    });

    expect(await downloadNames()).toEqual(['bob-DE.conf']);
  });

  // A cisco client ahead of bob takes bob's place in emails, and bob's own email is lost:
  // the page holds one name, and it is not his.
  it('never name a file after a client its remark does not carry', async () => {
    await renderSubPage({
      links: [wireguard('🇩🇪 De - bob')],
      emails: ['alice'],
      ciscoLogins: [{ email: 'alice', server: 'cisco.example.com', username: 'alice' }],
    });

    expect(await downloadNames()).toEqual(['De-bob-DE.conf']);
  });
});
