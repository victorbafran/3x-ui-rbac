import { afterEach, describe, expect, it } from 'vitest';
import { cleanup } from '@testing-library/react';

import SubCiscoCard from '@/pages/sub/SubCiscoCard';
import { renderWithProviders } from './test-utils';

const LOGIN = {
  email: 'customer@example.com',
  port: 4443,
  username: 'customer@example.com',
  password: 'pw123456',
};

function blocks(): string[] {
  return Array.from(document.querySelectorAll('.sub-cisco-login')).map(
    (dl) => dl.textContent ?? '',
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

  // One login, or the "off" setting, carries no label, and the card adds none.
  it('adds no heading to a login that has no label', () => {
    renderWithProviders(<SubCiscoCard logins={[{ ...LOGIN, server: 'tun.example.ir' }]} />);
    expect(blocks()).toHaveLength(1);
    expect(blocks()[0].startsWith('Server')).toBe(true);
  });
});
