import { describe, expect, it } from 'vitest';

import {
  amneziawgConfigFromLink,
  genAmneziaWGConfig,
  genWireguardConfig,
  wireguardConfigFromLink,
} from '@/lib/xray/inbound-link';
import type { AmneziawgInboundSettings } from '@/schemas/protocols/inbound/amneziawg';
import type { WireguardInboundSettings } from '@/schemas/protocols/inbound/wireguard';

// A value carrying a line break adds a line such as a root PostUp to the .conf;
// node sync, an import or an external subscription can each supply one.
const INJECTED = '140232109\nPostUp = touch /tmp/pwned';

const endpoint = { address: 'vpn.example.test', port: 51820, peerIndex: 0, remark: 'peer-1' };

function awgSettings(h1: string) {
  return {
    server: { publicKey: 'serverPubKey==', jc: 4, jmin: 40, jmax: 100, s1: 30, s2: 90, h1 },
    clients: [{ email: 'peer-1', privateKey: 'clientPrivKey==', allowedIPs: ['10.8.1.2/32'] }],
  } as unknown as AmneziawgInboundSettings;
}

function wgSettings(dns: string) {
  return {
    secretKey: '',
    mtu: 1420,
    dns,
    peers: [{ privateKey: 'clientPrivKey==', allowedIPs: ['10.0.0.2/32'] }],
  } as unknown as WireguardInboundSettings;
}

function vpnLink(conf: string): string {
  return `vpn://${btoa(conf).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
}

// The clients page's .conf files come from internal/sub (panel_configs.go), whose
// tests hold them to the same rule; these are the builders still in the browser.
describe('.conf builders render nothing when a value carries a line break', () => {
  it('genAmneziaWGConfig', () => {
    expect(genAmneziaWGConfig({ settings: awgSettings('1'), ...endpoint })).toContain('H1 = 1\n');
    expect(genAmneziaWGConfig({ settings: awgSettings(INJECTED), ...endpoint })).toBe('');
  });

  it('genWireguardConfig', () => {
    expect(genWireguardConfig({ settings: wgSettings('9.9.9.9'), ...endpoint })).toContain(
      'DNS = 9.9.9.9\n',
    );
    expect(genWireguardConfig({ settings: wgSettings(INJECTED), ...endpoint })).toBe('');
  });

  it('wireguardConfigFromLink', () => {
    const link = (address: string) =>
      `wireguard://PRIVKEY@203.0.113.9:51820/?address=${address}&publickey=PEERPUB&dns=1.1.1.1&allowedips=0.0.0.0%2F0`;
    expect(wireguardConfigFromLink(link('10.0.0.2%2F32'))).toContain('Address = 10.0.0.2/32\n');
    expect(
      wireguardConfigFromLink(link('10.0.0.2%2F32%0APostUp%20%3D%20touch%20%2Ftmp%2Fpwned')),
    ).toBe('');
  });
});

describe('amneziawgConfigFromLink', () => {
  // A vpn:// payload is a whole .conf from wherever the subscription got it;
  // no panel builder writes a wg-quick hook, so one in a payload was planted.
  it('refuses a payload that carries a wg-quick hook directive', () => {
    const clean = '[Interface]\nPrivateKey = k\nAddress = 10.8.1.2/32';
    expect(amneziawgConfigFromLink(vpnLink(clean))).toBe(clean);
    expect(amneziawgConfigFromLink(vpnLink(`${clean}\n  postup = touch /tmp/pwned`))).toBe('');
  });
});
