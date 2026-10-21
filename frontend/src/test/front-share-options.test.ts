import { describe, expect, it } from 'vitest';

import { frontShareOptions, type FrontOption } from '@/schemas/relay';

const front = (o: Partial<FrontOption>): FrontOption => ({
  id: 1,
  name: 'iran',
  label: '',
  address: '203.0.113.9',
  publicName: '',
  ready: true,
  ...o,
});

// The share address an inbound behind a front should hand out is the front's
// public name: a TLS handshake from inside Iran does not complete against a
// bare address. The address is offered only when there is no name.
describe('the share address suggestions', () => {
  it('offers the public name, labelled with the server it belongs to', () => {
    expect(frontShareOptions([front({ label: 'Iran', publicName: 'tun.example.ir' })])).toEqual([
      { value: 'tun.example.ir', label: 'Iran — tun.example.ir' },
    ]);
  });

  it('falls back to the address when the front has no name', () => {
    expect(frontShareOptions([front({})])).toEqual([
      { value: '203.0.113.9', label: 'iran — 203.0.113.9' },
    ]);
  });

  it('offers nothing for a front with neither', () => {
    expect(frontShareOptions([front({ address: '' })])).toEqual([]);
  });
});
