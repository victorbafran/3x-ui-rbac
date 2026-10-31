import { describe, expect, it } from 'vitest';

import { clientNameInRemark, flagCountryCode, wireguardFileNames } from '@/lib/inbounds/label';

// What the WireGuard app on Android accepts as a tunnel name, which it takes from the file.
const VALID = /^[a-zA-Z0-9_=+.-]{1,15}\.conf$/;

describe('flagCountryCode', () => {
  it('reads the country of the first flag in a name', () => {
    expect(flagCountryCode('in-45222-udp - 🇩🇪 De - Epic-vpnPromax')).toBe('DE');
    expect(flagCountryCode('🇺🇸 US · 🇩🇪 DE')).toBe('US');
  });

  it('finds no country without a whole flag', () => {
    expect(flagCountryCode('US · New York')).toBe('');
    expect(flagCountryCode('🇩 half a flag')).toBe('');
  });
});

describe('WireGuard .conf names', () => {
  it("put the config's country after the client name, cutting the name to fit", () => {
    expect(wireguardFileNames(['in-45222-udp - 🇩🇪 De - Epic-vpnPromax'], 'Epic-vpnPromax')).toEqual(
      ['Epic-vpnProm-DE.conf'],
    );
    expect(wireguardFileNames(['🇳🇱 NL'], 'x'.repeat(40))).toEqual(['xxxxxxxxxxxx-NL.conf']);
  });

  it('number the configs that share a country, in list order', () => {
    expect(wireguardFileNames(['🇩🇪 De · IPv6', '🇫🇷 Fr', '🇩🇪 De · IPv4'], 'Epic-vpnPromax')).toEqual(
      ['Epic-vpnPro-DE1.conf', 'Epic-vpnProm-FR.conf', 'Epic-vpnPro-DE2.conf'],
    );
  });

  it('number a config without a flag by its place, and leave a lone one bare', () => {
    expect(wireguardFileNames(['US · New York', '🇩🇪 De'], 'alice')).toEqual([
      'alice-1.conf',
      'alice-DE.conf',
    ]);
    expect(wireguardFileNames(['US · New York'], 'alice')).toEqual(['alice.conf']);
  });

  it('name each config after itself when no client name is given', () => {
    expect(wireguardFileNames(['🇩🇪 De - alice', 'bob'], '')).toEqual([
      'De-alice-DE.conf',
      'bob-2.conf',
    ]);
  });

  it('stay importable and distinct however long, decorated or alike the names are', () => {
    const configNames = [
      '🇩🇪 De',
      '🇩🇪 De',
      'plain',
      'plain',
      ...Array.from({ length: 10 }, (_, i) => `🇫🇷 Fr ${i}`),
      '🇺🇸 US - مشتری',
    ];
    for (const client of ['Epic-vpnPromax', 'مشتری تست', '---', 'a.b..c', 'x'.repeat(40), '']) {
      const names = wireguardFileNames(configNames, client);
      for (const name of names) expect(name).toMatch(VALID);
      expect(new Set(names.map((name) => name.toLowerCase())).size).toBe(configNames.length);
    }
  });
});

describe('clientNameInRemark', () => {
  it('finds the one client name a remark carries', () => {
    expect(clientNameInRemark('🇩🇪 De - bob', ['alice', 'bob'])).toBe('bob');
    expect(clientNameInRemark('in-45222-udp-Epic-vpnPromax-IPv4', ['Epic-vpnPromax'])).toBe(
      'Epic-vpnPromax',
    );
  });

  it('finds none in a remark that carries no name, or only part of a longer one', () => {
    expect(clientNameInRemark('🇩🇪 De - bob', ['alice'])).toBe('');
    expect(clientNameInRemark('🇩🇪 De - bobby', ['bob'])).toBe('');
    expect(clientNameInRemark('🇩🇪 De - bob.smith', ['bob'])).toBe('');
    expect(clientNameInRemark('🇩🇪 De - mybob', ['bob'])).toBe('');
    expect(clientNameInRemark('🇩🇪 De - bob', [])).toBe('');
  });

  it('finds none when the remark carries two names', () => {
    expect(clientNameInRemark('alice - bob', ['alice', 'bob'])).toBe('');
  });
});

describe('WireGuard .conf names, one base per config', () => {
  it('name each config after its own client, or its own remark where it has none', () => {
    expect(
      wireguardFileNames(['🇩🇪 De - bob', '🇩🇪 De - alice', 'carol'], ['bob', 'alice', '']),
    ).toEqual(['bob-DE1.conf', 'alice-DE2.conf', 'carol-3.conf']);
  });
});
