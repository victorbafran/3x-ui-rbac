import { describe, expect, it } from 'vitest';

import type { InboundClientLinks, TunnelConfig } from '@/generated/types';
import { inboundLinksExport } from '@/hooks/useInboundLinks';

function conf(email: string, text: string): TunnelConfig {
  return {
    inboundId: 7,
    protocol: 'wireguard',
    email,
    address: '',
    port: 51820,
    family: '',
    endpoint: '',
    remark: '',
    text,
  };
}

const ENTRIES: InboundClientLinks[] = [
  {
    email: 'alice',
    links: ['L-alice-6', 'L-alice-4'],
    configs: [conf('alice', 'C-alice-6'), conf('alice', 'C-alice-4')],
  },
  { email: 'bob', links: ['L-bob'], configs: [conf('bob', 'C-bob')] },
];

describe('inboundLinksExport', () => {
  it.each(['wireguard', 'amneziawg'])(
    'exports a %s inbound as its configs, with the links on their own tab',
    (protocol) => {
      expect(inboundLinksExport(protocol, ENTRIES)).toEqual({
        content: 'C-alice-6\r\nC-alice-4\r\nC-bob',
        configs: 'C-alice-6\r\nC-alice-4\r\nC-bob',
        links: 'L-alice-6\r\nL-alice-4\r\nL-bob',
      });
    },
  );

  it('exports any other inbound as its links alone, even when the clients carry configs', () => {
    expect(inboundLinksExport('tuic', ENTRIES)).toEqual({
      content: 'L-alice-6\r\nL-alice-4\r\nL-bob',
    });
  });
});
