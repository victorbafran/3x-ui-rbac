import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';

import SubOpenVPNCard from '@/pages/sub/SubOpenVPNCard';
import { renderWithProviders } from './test-utils';

const PROFILE = {
  email: 'customer@example.com',
  label: 'Germany',
  fileName: 'Germany.ovpn',
  url: 'https://vpn.example.com/sub/abc/openvpn/1.ovpn',
  username: 'customer@example.com',
  password: 'pw123456',
};

function stubObjectURL() {
  const created: Blob[] = [];
  const revoked: string[] = [];
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    writable: true,
    value: (blob: Blob) => {
      created.push(blob);
      return 'blob:stub';
    },
  });
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    writable: true,
    value: (href: string) => revoked.push(href),
  });
  return { created, revoked };
}

describe('the OpenVPN card on the subscription page', () => {
  beforeEach(() => {
    stubObjectURL();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  // A customer with no OpenVPN inbound should not be told about a protocol
  // they do not have.
  it('shows nothing when there is no profile', () => {
    const { container } = renderWithProviders(<SubOpenVPNCard profiles={[]} />);
    expect(container.textContent).toBe('');
  });

  // The whole point of the card: the file and the two values that go with it.
  // A file with no password is a download the customer cannot use.
  it('offers the file together with the name and password', () => {
    renderWithProviders(<SubOpenVPNCard profiles={[PROFILE]} />);
    expect(screen.getByText('Germany')).toBeTruthy();
    expect(screen.getByText('customer@example.com')).toBeTruthy();
    expect(screen.getByText('pw123456')).toBeTruthy();
  });

  // A customer with two countries has two files, and each has to be reachable
  // on its own.
  it('offers one download per country', () => {
    renderWithProviders(
      <SubOpenVPNCard
        profiles={[PROFILE, { ...PROFILE, label: 'Netherlands', url: '/sub/abc/openvpn/2.ovpn' }]}
      />,
    );
    expect(screen.getByText('Germany')).toBeTruthy();
    expect(screen.getByText('Netherlands')).toBeTruthy();
  });

  // The file is fetched and saved under the name the server chose, not under
  // whatever the browser makes of the URL's last path segment — which is the
  // inbound's id and tells the customer nothing.
  it('saves the file under the country name', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      blob: async () => new Blob(['client\n'], { type: 'application/x-openvpn-profile' }),
    });
    vi.stubGlobal('fetch', fetchMock);
    const clicked: HTMLAnchorElement[] = [];
    const realClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = function patched(this: HTMLAnchorElement) {
      clicked.push(this);
    };

    try {
      renderWithProviders(<SubOpenVPNCard profiles={[PROFILE]} />);
      fireEvent.click(screen.getByRole('button', { name: /download/i }));

      await waitFor(() => expect(clicked.length).toBe(1));
      expect(fetchMock).toHaveBeenCalledWith(PROFILE.url, { credentials: 'same-origin' });
      expect(clicked[0].download).toBe('Germany.ovpn');
    } finally {
      HTMLAnchorElement.prototype.click = realClick;
    }
  });

  // The one failure a customer can hit is an inbound that has never started,
  // so the server has no certificate to put in the file. A download that
  // silently does not happen is the worst possible answer to that.
  it('says so when the server has no file to give yet', async () => {
    // The body is a real one. A stub with no blob() would make this test
    // pass on the missing method rather than on the status, and the status
    // check could then be deleted without anything going red.
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 503,
        blob: async () => new Blob(['<html>503</html>'], { type: 'text/html' }),
      }),
    );
    const clicked: HTMLAnchorElement[] = [];
    const realClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = function patched(this: HTMLAnchorElement) {
      clicked.push(this);
    };

    try {
      renderWithProviders(<SubOpenVPNCard profiles={[PROFILE]} />);
      fireEvent.click(screen.getByRole('button', { name: /download/i }));

      await waitFor(() => expect(screen.getByText(/not ready yet/i)).toBeTruthy());
      expect(clicked.length).toBe(0);
    } finally {
      HTMLAnchorElement.prototype.click = realClick;
    }
  });
});
