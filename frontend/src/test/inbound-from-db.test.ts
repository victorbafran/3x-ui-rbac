import { describe, expect, it } from 'vitest';

import { inboundFromDb } from '@/lib/xray/inbound-from-db';
import { getInboundClients } from '@/lib/xray/inbound-link';
import { canEnableTlsFlow, isSS2022 } from '@/lib/xray/protocol-capabilities';

const BASE_DB_FIELDS = {
  port: 12345,
  listen: '',
  tag: '',
  remark: 'unit',
  enable: true,
  expiryTime: 0,
  up: 0,
  down: 0,
  total: 0,
  sniffing: '',
};

describe('inboundFromDb', () => {
  it('coerces JSON-string settings into a parsed object', () => {
    const raw = {
      ...BASE_DB_FIELDS,
      protocol: 'vless',
      settings: JSON.stringify({
        clients: [{ id: 'abc', email: 'a@test', flow: '' }],
        decryption: 'none',
      }),
      streamSettings: JSON.stringify({ network: 'tcp', security: 'none' }),
    };
    const inbound = inboundFromDb(raw);
    expect(inbound.protocol).toBe('vless');
    expect(inbound.port).toBe(12345);
    expect((inbound.settings as { decryption?: string }).decryption).toBe('none');
    expect((inbound.streamSettings as { network?: string })?.network).toBe('tcp');
  });

  it('fills schema defaults onto partial object settings', () => {
    const settings = { clients: [], decryption: 'none' };
    const raw = {
      ...BASE_DB_FIELDS,
      protocol: 'vless',
      settings,
      streamSettings: { network: 'ws', security: 'tls' },
    };
    const inbound = inboundFromDb(raw);
    // encryption/fallbacks defaulted by schema, original settings ref not preserved
    expect(inbound.settings).not.toBe(settings);
    expect((inbound.settings as { encryption?: string }).encryption).toBe('none');
    expect((inbound.streamSettings as { security?: string })?.security).toBe('tls');
  });

  // The settings parse strips keys the schema does not list, so a TUN option xray-core
  // 26.9.30 added and an admin entered as JSON would vanish on the next form save.
  it('keeps the TUN options xray-core 26.9 added', () => {
    const inbound = inboundFromDb({
      ...BASE_DB_FIELDS,
      protocol: 'tun',
      settings: {
        name: 'xray0',
        desc: 'MyTun',
        gateway: ['10.0.0.1/16'],
        autoSystemDnsToGateway: true,
        autoSystemWfpBlockLeak: ['dns', 'misconfigtun'],
      },
      streamSettings: '',
    });
    expect(inbound.settings).toMatchObject({
      desc: 'MyTun',
      autoSystemDnsToGateway: true,
      autoSystemWfpBlockLeak: ['dns', 'misconfigtun'],
    });
  });

  it('returns schema-default settings for missing/empty fields without throwing', () => {
    const raw = {
      ...BASE_DB_FIELDS,
      protocol: 'http',
      settings: '',
      streamSettings: '',
      sniffing: '',
    };
    const inbound = inboundFromDb(raw);
    // http settings has its own schema defaults (accounts: [], allowTransparent: false)
    expect(inbound.settings).toEqual(expect.objectContaining({ accounts: [] }));
    expect(inbound.streamSettings).toEqual({});
    expect(inbound.sniffing).toEqual({});
  });
});

describe('protocol-capability helpers with raw coerced shapes', () => {
  it('isSS2022 detects 2022-blake3 family', () => {
    expect(
      isSS2022({ protocol: 'shadowsocks', settings: { method: '2022-blake3-aes-128-gcm' } }),
    ).toBe(true);
    expect(isSS2022({ protocol: 'shadowsocks', settings: { method: 'aes-256-gcm' } })).toBe(false);
  });

  it('canEnableTlsFlow gates on vless + tcp + tls/reality', () => {
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        streamSettings: { network: 'tcp', security: 'tls' },
      }),
    ).toBe(true);
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        streamSettings: { network: 'ws', security: 'tls' },
      }),
    ).toBe(false);
    expect(
      canEnableTlsFlow({
        protocol: 'vmess',
        streamSettings: { network: 'tcp', security: 'tls' },
      }),
    ).toBe(false);
  });

  it('canEnableTlsFlow allows vless + xhttp when vlessenc encryption is set', () => {
    const enc = 'mlkem768x25519plus.native.0rtt.G3cdPSd1-NnlpTbWNSM5vHsT5VNzWfFzYSKwbUMnV1Y';
    const dec = 'mlkem768x25519plus.native.600s.mMFxPe7lz5xoq2qBk22cQYefu5fpc_2dGR8lMOKem0E';
    // XHTTP + a real (generated) encryption value → Vision flow allowed.
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        settings: { encryption: enc },
        streamSettings: { network: 'xhttp', security: 'none' },
      }),
    ).toBe(true);
    // decryption alone (server-side value) is enough on XHTTP.
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        settings: { decryption: dec, encryption: 'none' },
        streamSettings: { network: 'xhttp', security: 'none' },
      }),
    ).toBe(true);
    // No encryption → stays gated off.
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        settings: { encryption: 'none' },
        streamSettings: { network: 'xhttp', security: 'none' },
      }),
    ).toBe(false);
    // xray-core runs Vision inside VLESS encryption on any transport, RAW and WS included,
    // but XDRIVE, which refuses a flow since 26.10.17 (#7182).
    for (const network of ['tcp', 'ws', 'grpc']) {
      expect(
        canEnableTlsFlow({
          protocol: 'vless',
          settings: { decryption: dec, encryption: enc },
          streamSettings: { network, security: 'none' },
        }),
      ).toBe(true);
    }
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        settings: { decryption: dec, encryption: enc },
        streamSettings: { network: 'xdrive', security: 'none' },
      }),
    ).toBe(false);
    expect(
      canEnableTlsFlow({
        protocol: 'vless',
        settings: { encryption: 'none' },
        streamSettings: { network: 'ws', security: 'tls' },
      }),
    ).toBe(false);
  });
});

describe('getInboundClients with schema-shaped inbound', () => {
  it('returns clients array for vless/vmess/trojan/hysteria', () => {
    const inbound = inboundFromDb({
      ...BASE_DB_FIELDS,
      protocol: 'vless',
      settings: { clients: [{ id: 'x', email: 'e@test' }], decryption: 'none' },
      streamSettings: { network: 'tcp', security: 'none' },
    });
    expect(getInboundClients(inbound)).toHaveLength(1);
  });

  it('returns null for non-client protocols (http/mixed/tun/tunnel)', () => {
    for (const protocol of ['http', 'mixed', 'tun', 'tunnel']) {
      const inbound = inboundFromDb({
        ...BASE_DB_FIELDS,
        protocol,
        settings: {},
        streamSettings: '',
      });
      expect(getInboundClients(inbound)).toBeNull();
    }
  });
});
