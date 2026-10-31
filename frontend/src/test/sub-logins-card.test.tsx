import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen } from '@testing-library/react';

import SubLoginsCard from '@/pages/sub/SubLoginsCard';
import { renderWithProviders } from './test-utils';

const ALI = { email: 'ali', username: 'ali', password: 'pw-ali' };
const REZA = { email: 'reza', username: 'reza', password: 'pw-reza' };
const cisco = (server: string, who = ALI) => ({ ...who, server, port: 443 });
const ovpn = (label: string, who = ALI) => ({
  ...who,
  label,
  fileName: `${label}.ovpn`,
  url: `/sub/abc/openvpn/${label}.ovpn`,
});
const SHARED_NOTE = /work for both Cisco and OpenVPN/;

function groups(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('.sub-login-group'));
}

describe('the cisco and OpenVPN logins card', () => {
  afterEach(() => cleanup());

  // The owner's screenshot: the same name and password under every server and
  // every file. One client's pair is shown once, below everything it opens.
  it('shows a login shared by both protocols once, with the note that it opens both', () => {
    renderWithProviders(
      <SubLoginsCard
        logins={[cisco('ali.example.ir'), cisco('v4.example.ir')]}
        profiles={[ovpn('DE Germany'), ovpn('FI Finland')]}
        onCopy={() => {}}
      />,
    );
    expect(screen.getAllByText('ali')).toHaveLength(1);
    expect(screen.getAllByText('pw-ali')).toHaveLength(1);
    expect(screen.getAllByText(SHARED_NOTE)).toHaveLength(1);
    expect(screen.getByText('Cisco AnyConnect & OpenVPN')).toBeTruthy();
  });

  // With one protocol there is nothing for the login to be shared with.
  it('shows a single protocol’s login once and makes no claim about the other', () => {
    renderWithProviders(
      <SubLoginsCard
        logins={[cisco('ali.example.ir'), cisco('v4.example.ir')]}
        profiles={[]}
        onCopy={() => {}}
      />,
    );
    expect(screen.getAllByText('pw-ali')).toHaveLength(1);
    expect(screen.queryByText(SHARED_NOTE)).toBeNull();
  });

  // Two clients on one subscription: each pair sits under its own rows, and only
  // the group that really holds both protocols says the login opens both.
  it('keeps two clients’ logins apart and notes "both" only where it is true', () => {
    renderWithProviders(
      <SubLoginsCard
        logins={[cisco('a.example.ir'), cisco('b.example.ir', REZA)]}
        profiles={[ovpn('DE Germany')]}
        onCopy={() => {}}
      />,
    );
    const [aliGroup, rezaGroup] = groups();
    expect(groups()).toHaveLength(2);
    expect(aliGroup.textContent).toContain('a.example.ir');
    expect(aliGroup.textContent).toContain('DE Germany');
    expect(aliGroup.textContent).toContain('pw-ali');
    expect(aliGroup.textContent).toMatch(SHARED_NOTE);
    expect(rezaGroup.textContent).toContain('b.example.ir');
    expect(rezaGroup.textContent).toContain('pw-reza');
    expect(rezaGroup.textContent).not.toContain('pw-ali');
    expect(rezaGroup.textContent).not.toMatch(SHARED_NOTE);
    // The app links are where to get a client, said once however many groups there are.
    expect(screen.getAllByRole('link', { name: /Cisco client \(Android\)/ })).toHaveLength(1);
  });

  // The password is the value typed by hand most often wrong; its button copies it,
  // not the username beside it.
  it('copies the password from the password’s own button', () => {
    const onCopy = vi.fn();
    renderWithProviders(
      <SubLoginsCard logins={[cisco('a.example.ir')]} profiles={[]} onCopy={onCopy} />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Copy: Password' }));
    expect(onCopy).toHaveBeenLastCalledWith('pw-ali');
  });
});
