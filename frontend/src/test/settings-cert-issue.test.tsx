import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';

import { HttpUtil } from '@/utils';
import { AllSetting } from '@/models/setting';
import { certNameFrom, isAddress } from '@/components/CertIssueControl';
import GeneralTab from '@/pages/settings/GeneralTab';
import SubscriptionGeneralTab from '@/pages/settings/SubscriptionGeneralTab';
import { renderWithProviders } from './test-utils';

const issued = (name: string) => ({
  success: true,
  msg: '',
  obj: {
    certFile: `/etc/x-ui/certs/${name}/fullchain.pem`,
    keyFile: `/etc/x-ui/certs/${name}/privkey.pem`,
    names: [name],
    notAfter: '2027-01-19T13:31:06Z',
    reused: false,
  },
});

// Opens the Certificates tab and presses through the dialog with the names it
// suggested, so the test sees what the operator would get without typing.
async function issueFromCertificatesTab() {
  fireEvent.click(screen.getByRole('tab', { name: /Certificates/ }));
  await act(async () => {});
  fireEvent.click(screen.getByRole('button', { name: /Get a certificate/ }));
  await act(async () => {});
  fireEvent.click(screen.getByText('Get the certificate'));
  await act(async () => {});
}

describe('getting a certificate from the settings', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    cleanup();
  });

  // The panel's own name is offered, and the two path fields fill themselves.
  it('fills the panel certificate fields with the one it got', async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(issued('panel.example.ir') as never);
    const updateSetting = vi.fn();
    renderWithProviders(
      <MemoryRouter initialEntries={['/settings']}>
        <GeneralTab
          allSetting={new AllSetting({ webDomain: 'panel.example.ir' })}
          updateSetting={updateSetting}
        />
      </MemoryRouter>,
    );
    await act(async () => {});
    await issueFromCertificatesTab();

    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/issueCert',
      { names: ['panel.example.ir'] },
      expect.anything(),
    );
    expect(updateSetting).toHaveBeenCalledWith({
      webCertFile: '/etc/x-ui/certs/panel.example.ir/fullchain.pem',
      webKeyFile: '/etc/x-ui/certs/panel.example.ir/privkey.pem',
    });
    // The operator is told the certificate only takes after a save and a restart.
    expect(await screen.findByText(/Save the settings, then restart the panel/)).toBeTruthy();
  });

  // With no subscription domain, the host customers' links carry is offered.
  it('fills the subscription certificate fields from the link host', async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(issued('sub.example.ir') as never);
    const updateSetting = vi.fn();
    renderWithProviders(
      <MemoryRouter initialEntries={['/settings#subscription']}>
        <SubscriptionGeneralTab
          allSetting={new AllSetting({ subURI: 'https://sub.example.ir:2096/sub/' })}
          updateSetting={updateSetting}
        />
      </MemoryRouter>,
    );
    await act(async () => {});
    await issueFromCertificatesTab();

    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/issueCert',
      { names: ['sub.example.ir'] },
      expect.anything(),
    );
    expect(updateSetting).toHaveBeenCalledWith({
      subCertFile: '/etc/x-ui/certs/sub.example.ir/fullchain.pem',
      subKeyFile: '/etc/x-ui/certs/sub.example.ir/privkey.pem',
    });
  });

  // Opened by IP, the panel's note says to come back by name after the restart.
  it('tells an address from a name', () => {
    expect(isAddress('212.95.35.41')).toBe(true);
    expect(isAddress('[2001:db8::1]')).toBe(true);
    expect(isAddress('panel.example.ir')).toBe(false);
    expect(isAddress('localhost')).toBe(false);
  });

  // An address is not something this issuer asks for, so it is never offered.
  it('offers a name, never an address', () => {
    expect(certNameFrom('212.95.35.41', 'https://sub.example.ir:2096/x')).toEqual([
      'sub.example.ir',
    ]);
    expect(certNameFrom('panel.example.ir:2053')).toEqual(['panel.example.ir']);
    expect(certNameFrom('', 'http://[2001:db8::1]:80/', undefined)).toEqual([]);
    expect(certNameFrom('not a url://')).toEqual([]);
  });
});
