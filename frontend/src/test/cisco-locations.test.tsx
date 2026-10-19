import { describe, expect, it, vi } from 'vitest';
import { Form } from 'antd';
import type { ReactNode } from 'react';
import { FormProvider, useForm } from 'react-hook-form';
import { cleanup, fireEvent, screen } from '@testing-library/react';

import CiscoFields from '@/pages/inbounds/form/protocols/cisco';
import { createDefaultCiscoInboundSettings } from '@/lib/xray/inbound-defaults';
import { CiscoInboundSettingsSchema } from '@/schemas/protocols/inbound/cisco';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import { renderWithProviders } from './test-utils';

// The panel already asked each exit server where it was, so picking one in
// the form should name the country — flag and all — without the operator
// typing it.
vi.mock('@/api/queries/useRelaysQuery', () => ({
  useRelayOptions: () => ({
    options: [
      {
        interface: '',
        name: 'local',
        label: '🇩🇪 Germany',
        address: '',
        country: 'Germany',
        countryCode: 'DE',
        ready: true,
      },
      {
        interface: 'wgr2',
        name: 'ams',
        label: '🇳🇱 Netherlands',
        address: '198.51.100.7',
        country: 'Netherlands',
        countryCode: 'NL',
        ready: true,
      },
    ],
  }),
  useRelaysQuery: () => ({
    relays: [],
    isFetched: true,
    isLoading: false,
    error: null,
    refetch: () => {},
  }),
}));

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

  // Picking an exit is the moment the country can be named for free.
  it('names the country and its flag when an exit is picked', () => {
    renderCisco(createDefaultCiscoInboundSettings());
    const add = Array.from(document.querySelectorAll<HTMLElement>('button[aria-label="Add"]')).at(
      -1,
    );
    fireEvent.click(add as HTMLElement);

    const exitInput = Array.from(document.querySelectorAll<HTMLInputElement>('input')).at(-1);
    expect(exitInput).toBeTruthy();
    // Opening the list is enough; typing would re-render the row under the
    // dropdown and close it before anything could be clicked.
    fireEvent.mouseDown(exitInput as HTMLElement);
    fireEvent.focus(exitInput as HTMLElement);

    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      (el.textContent ?? '').includes('Netherlands'),
    );
    expect(option, 'the enrolled exits should be offered').toBeTruthy();
    fireEvent.click(option as HTMLElement);

    // The two name boxes are gone for a country backed by a server: it is
    // named after the server, and the row shows what the customer reads.
    expect(countryNames()).toHaveLength(0);
    const shown = Array.from(document.querySelectorAll<HTMLInputElement>('input[readonly]')).map(
      (el) => el.value,
    );
    expect(shown).toContain('🇳🇱 Netherlands');
    cleanup();
  });

  // The country this panel carries itself deserves its flag as much as any
  // other, and it is the one the operator may want to sell under a name of
  // their own — so it, and only it, keeps a way back to the boxes.
  it('names the country this server carries, and lets it be renamed', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        { name: 'germany', label: '🇩🇪 Germany', exit: '', network: '', netmask: '', dns: [] },
      ],
    });
    expect(countryNames()).toHaveLength(0);
    const shown = Array.from(document.querySelectorAll<HTMLInputElement>('input[readonly]')).map(
      (el) => el.value,
    );
    expect(shown).toContain('🇩🇪 Germany');

    fireEvent.click(screen.getByText('Rename'));
    expect(countryNames()).toEqual(['germany']);
    cleanup();
  });

  // A country behind an enrolled server is named after that server, and there
  // is nothing to rename: changing it there would only disagree with the
  // server it points at.
  it('offers no rename for a country behind an exit server', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        { name: 'netherlands', label: 'x', exit: 'wgr2', network: '', netmask: '', dns: [] },
      ],
    });
    expect(screen.queryByText('Rename')).toBeNull();
    cleanup();
  });

  // An inbound that already has countries must show them, or an operator
  // editing it would believe they had been lost and add them a second time.
  it('shows the countries an inbound already has', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        {
          name: 'germany',
          label: '\u{1F1E9}\u{1F1EA} Germany',
          exit: '',
          network: '',
          netmask: '',
          dns: [],
        },
        // An interface the panel did not put there — an operator's own tunnel.
        // It has no server to be named after, so it keeps its boxes.
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
    expect(countryNames()).toEqual(['netherlands']);
    const shown = Array.from(document.querySelectorAll<HTMLInputElement>('input[readonly]')).map(
      (el) => el.value,
    );
    expect(shown).toContain('\u{1F1E9}\u{1F1EA} Germany');
    cleanup();
  });

  // Removing a country has to take the row with it. A row left on screen reads
  // as a country that is still offered, and the operator saves it back.
  it('removes a country row on demand', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        { name: 'germany', label: 'Germany', exit: 'wg0', network: '', netmask: '', dns: [] },
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
