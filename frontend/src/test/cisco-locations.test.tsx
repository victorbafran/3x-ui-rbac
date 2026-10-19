import { describe, expect, it } from 'vitest';
import { Form } from 'antd';
import type { ReactNode } from 'react';
import { FormProvider, useForm } from 'react-hook-form';
import { cleanup, fireEvent, screen } from '@testing-library/react';

import CiscoFields from '@/pages/inbounds/form/protocols/cisco';
import { createDefaultCiscoInboundSettings } from '@/lib/xray/inbound-defaults';
import { CiscoInboundSettingsSchema } from '@/schemas/protocols/inbound/cisco';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import { renderWithProviders } from './test-utils';

function FormHarness({
  children,
  defaultValues,
}: {
  children: ReactNode;
  defaultValues?: Record<string, unknown>;
}) {
  const methods = useForm<InboundFormValues>({ defaultValues: defaultValues as never });
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

function renderCisco(settings: Record<string, unknown>) {
  return renderWithProviders(
    <FormHarness defaultValues={{ port: 443, settings }}>
      <CiscoFields />
    </FormHarness>,
  );
}

function countryNames(): string[] {
  return Array.from(
    document.querySelectorAll<HTMLInputElement>('input[placeholder="name, e.g. netherlands"]'),
  ).map((el) => el.value);
}

describe('the countries a cisco inbound offers', () => {
  // A new inbound must offer none. Once ocserv has any select-group it refuses
  // every client that does not name a group, so a country list created by
  // default would take a brand new inbound offline for everyone on it.
  it('starts empty on a new inbound', () => {
    expect(createDefaultCiscoInboundSettings().locations).toEqual([]);
  });

  // The stored shape has to survive a round trip, or an operator's countries
  // would quietly vanish the next time the inbound was saved.
  it('keeps what the operator typed', () => {
    const parsed = CiscoInboundSettingsSchema.parse({
      locations: [{ name: 'netherlands', label: 'Netherlands', exit: 'wg0' }],
    });
    expect(parsed.locations).toEqual([
      { name: 'netherlands', label: 'Netherlands', exit: 'wg0', network: '', netmask: '', dns: [] },
    ]);
  });

  // A client that names no country is entitled to all of them, so the field
  // has to default to an empty list rather than be dropped: a missing list and
  // an empty one must mean the same thing on the way back in.
  it('leaves a client entitled to every country by default', () => {
    const parsed = CiscoInboundSettingsSchema.parse({
      clients: [{ email: 'a@b', password: 'x' }],
    });
    expect(parsed.clients[0]?.locations).toEqual([]);
  });

  it('shows the operator the country list', () => {
    renderCisco(createDefaultCiscoInboundSettings());
    expect(screen.getByText('Countries offered')).toBeTruthy();
    cleanup();
  });

  // Adding a row is the whole interaction: the operator presses the button and
  // types a country. If the row never reaches settings.locations the dropdown
  // stays empty however much they type.
  it('adds a country row on demand', () => {
    renderCisco(createDefaultCiscoInboundSettings());
    expect(countryNames()).toHaveLength(0);

    const add = Array.from(document.querySelectorAll<HTMLElement>('button[aria-label="Add"]')).at(
      -1,
    );
    expect(add).toBeTruthy();
    fireEvent.click(add as HTMLElement);

    expect(countryNames()).toHaveLength(1);
    // The exit field is an AutoComplete so the panel can suggest the servers
    // it has already enrolled; antd renders that placeholder as a span rather
    // than on the input, so the row is checked by what the operator sees.
    expect(document.body.textContent).toContain('exit, e.g. wg0');
    cleanup();
  });

  // An inbound that already has countries must show them, or an operator
  // editing it would believe they had been lost and add them a second time.
  it('shows the countries an inbound already has', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        { name: 'germany', label: 'Germany', exit: '', network: '', netmask: '', dns: [] },
        {
          name: 'netherlands',
          label: 'Netherlands',
          exit: 'wg0',
          network: '',
          netmask: '',
          dns: [],
        },
      ],
    });
    expect(countryNames()).toEqual(['germany', 'netherlands']);
    cleanup();
  });

  // Removing a country has to take the row with it. A row left on screen reads
  // as a country that is still offered, and the operator saves it back.
  it('removes a country row on demand', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        { name: 'germany', label: 'Germany', exit: '', network: '', netmask: '', dns: [] },
      ],
    });
    expect(countryNames()).toEqual(['germany']);

    const remove = Array.from(
      document.querySelectorAll<HTMLElement>('button[aria-label="Remove"]'),
    ).at(-1);
    expect(remove).toBeTruthy();
    fireEvent.click(remove as HTMLElement);

    expect(countryNames()).toHaveLength(0);
    cleanup();
  });
});
