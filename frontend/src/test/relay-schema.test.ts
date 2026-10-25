import { describe, expect, it } from 'vitest';

import { RelayListSchema } from '@/schemas/relay';

// A panel older than IPv6 fronts sends rows without the three fields. If the
// row fails to parse, parseMsg hands the page the raw response instead.
describe('a relay row from a panel before IPv6 fronts', () => {
  it('parses, reading as an IPv4-only front with no IPv6 address', () => {
    const parsed = RelayListSchema.safeParse({
      obj: [{ id: 3, name: 'iran-front', address: '203.0.113.9', role: 'front' }],
    });
    expect(parsed.success).toBe(true);
    const row = parsed.data?.obj?.[0];
    expect([row?.address6, row?.ipFamily, row?.address6Error, row?.publicName6]).toEqual([
      '',
      '',
      '',
      '',
    ]);
  });

  it('keeps what a newer panel sends', () => {
    const row = RelayListSchema.parse({
      obj: [
        {
          id: 3,
          name: 'iran-front',
          address: '203.0.113.9',
          role: 'front',
          address6: '2001:db8::9',
          ipFamily: 'both',
          address6Error: 'dial tcp [2001:db8::9]:443: i/o timeout',
          publicName6: 'v6.example.ir',
        },
      ],
    }).obj?.[0];
    expect([row?.address6, row?.ipFamily, row?.address6Error, row?.publicName6]).toEqual([
      '2001:db8::9',
      'both',
      'dial tcp [2001:db8::9]:443: i/o timeout',
      'v6.example.ir',
    ]);
  });
});
