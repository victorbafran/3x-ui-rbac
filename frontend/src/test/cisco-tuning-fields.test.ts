import { describe, expect, it } from 'vitest';

import { CiscoInboundSettingsSchema } from '@/schemas/protocols/inbound/cisco';
import { OpenVPNInboundSettingsSchema } from '@/schemas/protocols/inbound/openvpn';
import { InboundSettingsSchema } from '@/schemas/protocols/inbound';

// On 2026-10-21 a cisco inbound behind an entry server had its MTU set to
// 1300, the value measured to stop large packets vanishing in the tunnel. The
// owner then saved the inbound through the form to set a certificate, and the
// MTU was back at 1400: the form's schema did not know the field, so the
// validation that runs on save dropped it.

describe('saving a cisco inbound keeps its tuning', () => {
  const stored = {
    clients: [],
    mtu: 1300,
    mtuDiscovery: false,
    mobileDpd: 90,
  };

  it('keeps the MTU and the mobile DPD through the schema', () => {
    const parsed = CiscoInboundSettingsSchema.parse(stored);
    expect(parsed.mtu).toBe(1300);
    expect(parsed.mobileDpd).toBe(90);
    expect(parsed.mtuDiscovery).toBe(false);
  });

  it('keeps them through the discriminated union the form validates with', () => {
    const parsed = InboundSettingsSchema.parse({ protocol: 'cisco', settings: stored });
    expect(parsed.settings).toMatchObject({ mtu: 1300, mobileDpd: 90 });
  });

  // A setting the panel reads but the form does not show yet, set through the
  // API, must survive a save through the form too, or the next field anybody
  // adds on the Go side is lost the same way.
  it('does not drop a setting the form does not know about', () => {
    const parsed = CiscoInboundSettingsSchema.parse({ ...stored, laterKnob: 7 });
    expect((parsed as Record<string, unknown>).laterKnob).toBe(7);
  });

  it('leaves a fresh inbound on the server defaults', () => {
    const parsed = CiscoInboundSettingsSchema.parse({ clients: [] });
    // Zero means "the server's own default", 1400 and 1800 seconds.
    expect(parsed.mtu).toBe(0);
    expect(parsed.mobileDpd).toBe(0);
    expect(parsed.mtuDiscovery).toBe(false);
  });
});

describe('saving an openvpn inbound keeps settings the form does not show', () => {
  it('does not drop a setting the form does not know about', () => {
    const parsed = OpenVPNInboundSettingsSchema.parse({ clients: [], laterKnob: 'x' });
    expect((parsed as Record<string, unknown>).laterKnob).toBe('x');
  });
});
