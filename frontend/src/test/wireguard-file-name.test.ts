import { describe, expect, it } from 'vitest';

import { formatTunnelConfigMeta, wireguardTunnelName } from '@/lib/inbounds/label';

// What the WireGuard app on Android accepts as a tunnel name, which it takes from the file.
const VALID = /^[a-zA-Z0-9_=+.-]{1,15}$/;
const stem = (fileName: string) => fileName.replace(/\.conf$/, '');

describe('WireGuard .conf names', () => {
  it('stay importable however long or decorated the source is', () => {
    for (const base of [
      'Epic-vpnPromax',
      'in-45222-udp - 🇩🇪 De - Epic-vpnPromax',
      'مشتری تست',
      '---',
      'a.b..c',
      'x'.repeat(40),
    ]) {
      for (const n of [undefined, 1, 12]) {
        expect(wireguardTunnelName(base, n)).toMatch(VALID);
      }
    }
  });

  it('keep the client name when it fits and number the copies', () => {
    expect(wireguardTunnelName('Epic-vpnPromax')).toBe('Epic-vpnPromax');
    expect(wireguardTunnelName('Epic-vpnPromax', 2)).toBe('Epic-vpnProma-2');
    expect(wireguardTunnelName('🇩🇪 De - Epic', 1)).toBe('De-Epic-1');
  });

  it('give each of a client’s configs its own valid file', () => {
    const inbound = { id: 9, tag: 'in-45222-udp', remark: '' };
    const names = ['v6.aliwoodbaf.ir:45222', 'bemanche.aliwoodbaf.ir:45222'].map(
      (endpoint, i) => formatTunnelConfigMeta(inbound, 'Epic-vpnPromax', 2, endpoint, i).fileName,
    );
    for (const name of names) {
      expect(name.endsWith('.conf')).toBe(true);
      expect(stem(name)).toMatch(VALID);
    }
    expect(new Set(names).size).toBe(2);
    expect(stem(formatTunnelConfigMeta(inbound, 'Epic-vpnPromax').fileName)).toBe('Epic-vpnPromax');
  });
});
