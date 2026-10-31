import { afterEach, describe, expect, it } from 'vitest';
import { cleanup } from '@testing-library/react';

import SubLoginsCard from '@/pages/sub/SubLoginsCard';
import type { CiscoLogin } from '@/pages/sub/subPageModel';
import { renderWithProviders } from './test-utils';

const LOGIN = {
  email: 'customer@example.com',
  port: 4443,
  username: 'customer@example.com',
  password: 'pw123456',
};

function SubCiscoCard({ logins }: { logins: CiscoLogin[] }) {
  return <SubLoginsCard logins={logins} profiles={[]} onCopy={() => {}} />;
}

function blocks(): string[] {
  return Array.from(document.querySelectorAll('.sub-login-row')).map(
    (row) => row.textContent ?? '',
  );
}

describe('the cisco card on the subscription page', () => {
  afterEach(() => cleanup());

  // A front set to "both" hands one customer two logins that differ only in the
  // server; the label is how they know which is the IPv6 one to try first.
  it('heads each of a front’s two logins with the label the panel sent', () => {
    renderWithProviders(
      <SubCiscoCard
        logins={[
          { ...LOGIN, server: 'v6.example.ir', label: 'IPv6' },
          { ...LOGIN, server: 'tun.example.ir', label: 'IPv4' },
        ]}
      />,
    );
    const [first, second] = blocks();
    expect(first.startsWith('IPv6')).toBe(true);
    expect(first).toContain('v6.example.ir:4443');
    expect(second.startsWith('IPv4')).toBe(true);
    expect(second).toContain('tun.example.ir:4443');
  });

  // A pair labelled by family says it in the page's own language rather than the
  // panel's English mark; a numbered pair keeps the numbers it was sent.
  it('names a login’s family in its own words when the panel says which', () => {
    renderWithProviders(
      <SubCiscoCard
        logins={[
          { ...LOGIN, server: 'ipv6.example.ir', label: 'IPv6', family: 'v6' },
          { ...LOGIN, server: 'vpn.example.ir', label: 'IPv4', family: 'v4' },
          { ...LOGIN, server: 'tun.example.ir', label: '2' },
        ]}
      />,
    );
    const [v6, v4, numbered] = blocks();
    expect(v6.startsWith('Over IPv6')).toBe(true);
    expect(v4.startsWith('Over IPv4')).toBe(true);
    expect(numbered.startsWith('2')).toBe(true);
  });

  // One login, or the "off" setting, carries no label; the row is then just the server.
  it('names a login that has no label plainly as the server', () => {
    renderWithProviders(<SubCiscoCard logins={[{ ...LOGIN, server: 'tun.example.ir' }]} />);
    expect(blocks()).toHaveLength(1);
    expect(blocks()[0].startsWith('Server')).toBe(true);
  });

  // cisco now listens on IPv6 too, and "2001:db8::7:4443" is a different address,
  // not a port; a front's address arrives bracketed already and stays as it is.
  it('brackets an IPv6 address before its port', () => {
    renderWithProviders(
      <SubCiscoCard
        logins={[
          { ...LOGIN, server: '2001:db8::7' },
          { ...LOGIN, server: '[2001:db8::8]' },
        ]}
      />,
    );
    const [bare, bracketed] = blocks();
    expect(bare).toContain('[2001:db8::7]:4443');
    expect(bracketed).toContain('[2001:db8::8]:4443');
    expect(bracketed).not.toContain('[[');
  });
});
