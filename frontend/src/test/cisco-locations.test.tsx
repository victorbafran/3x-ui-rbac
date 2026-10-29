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
      {
        interface: 'psi-AT',
        name: 'Psiphon AT',
        label: '🇦🇹 Austria',
        address: '',
        country: 'Austria',
        countryCode: 'AT',
        ready: true,
        kind: 'psiphon',
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

// One exit picker per row, in row order.
function exitPickers(): HTMLInputElement[] {
  return Array.from(
    document.querySelectorAll<HTMLInputElement>('input[aria-label="exit, e.g. wg0"]'),
  );
}

// Read from that picker's own dropdown, so a list another row left open
// cannot answer for it.
function offeredOn(row: number): string[] {
  const picker = exitPickers()[row] as HTMLElement;
  fireEvent.mouseDown(picker);
  fireEvent.focus(picker);
  const list = document.getElementById(picker.getAttribute('aria-controls') ?? '');
  const dropdown = list?.closest('.ant-select-dropdown');
  expect(dropdown, `row ${row} should open its own exit list`).toBeTruthy();
  return Array.from(
    dropdown?.querySelectorAll('.ant-select-item-group, .ant-select-item-option') ?? [],
  ).map((el) => el.textContent ?? '');
}

const GERMANY = '🇩🇪 Germany — ';
const NETHERLANDS = '🇳🇱 Netherlands — 198.51.100.7';

function location(name: string, label: string, exit: string) {
  return { name, label, exit, network: '', netmask: '', dns: [] };
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

  // A Psiphon country is stored by its exit name like a relay's interface: offered
  // after the exit servers, and naming the row for its country.
  it('offers the Psiphon countries under their own heading and names the row', () => {
    renderCisco(createDefaultCiscoInboundSettings());
    const add = Array.from(document.querySelectorAll<HTMLElement>('button[aria-label="Add"]')).at(
      -1,
    );
    fireEvent.click(add as HTMLElement);
    const exitInput = Array.from(document.querySelectorAll<HTMLInputElement>('input')).at(-1);
    fireEvent.mouseDown(exitInput as HTMLElement);
    fireEvent.focus(exitInput as HTMLElement);

    const listed = Array.from(
      document.querySelectorAll('.ant-select-item-group, .ant-select-item-option'),
    ).map((el) => el.textContent ?? '');
    expect(listed.slice(-2)).toEqual(['Psiphon', '🇦🇹 Austria']);
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      (el.textContent ?? '').includes('Austria'),
    );
    fireEvent.click(option as HTMLElement);

    expect(countryNames()).toHaveLength(0);
    const shown = Array.from(document.querySelectorAll<HTMLInputElement>('input[readonly]')).map(
      (el) => el.value,
    );
    expect(shown).toContain('🇦🇹 Austria');
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

  // An exit already used on one row is not offered on the others, so the operator
  // sees what is taken; the row using it keeps it, and an emptied heading goes too.
  it('offers each exit on the row that uses it and on no other', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        location('netherlands', '🇳🇱 Netherlands', 'wgr2'),
        location('austria', '🇦🇹 Austria', 'psi-AT'),
        location('', '', ''),
      ],
    });
    expect(offeredOn(2)).toEqual([GERMANY]);
    // A picker opens filtered by the exit it holds, so a used row lists just its own.
    expect(offeredOn(0)).toEqual([NETHERLANDS]);
    expect(offeredOn(1)).toEqual(['Psiphon', '🇦🇹 Austria']);
    cleanup();
  });

  // Typing over a row's exit to change its country is when a used one would slip back
  // in: the list it filters is still only what no other row uses.
  it('offers a used exit on no other row while one is being typed', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        location('netherlands', '🇳🇱 Netherlands', 'wgr2'),
        location('austria', '🇦🇹 Austria', 'psi-AT'),
      ],
    });
    fireEvent.change(exitPickers()[1] as HTMLElement, { target: { value: 'an' } });
    expect(offeredOn(1)).toEqual([GERMANY]);
    cleanup();
  });

  // Rows saved before this rule may share an exit. Each keeps its own in its
  // picker, or reopening one would offer nothing for the exit it already has.
  it('keeps a shared exit offered on each row that has it', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [
        location('netherlands', '🇳🇱 Netherlands', 'wgr2'),
        location('holland', '🇳🇱 Holland', 'wgr2'),
      ],
    });
    expect(offeredOn(0)).toEqual([NETHERLANDS]);
    expect(offeredOn(1)).toEqual([NETHERLANDS]);
    cleanup();
  });

  // Clearing a row's exit hands it back at once, which holds only if the
  // pickers follow the rows as they are now rather than as they loaded.
  it('offers an exit again once its row lets go of it', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [location('netherlands', '🇳🇱 Netherlands', 'wgr2'), location('', '', '')],
    });
    expect(offeredOn(1)).not.toContain(NETHERLANDS);

    fireEvent.change(exitPickers()[0] as HTMLElement, { target: { value: '' } });
    expect(offeredOn(1)).toContain(NETHERLANDS);
    cleanup();
  });

  // An empty exit is this server, but only once the row has a name: a fresh
  // row has picked nothing yet and must not hide this server from the others.
  it('takes this server only for a row that leaves through it', () => {
    renderCisco({
      ...createDefaultCiscoInboundSettings(),
      locations: [location('', '', ''), location('', '', '')],
    });
    expect(offeredOn(1)).toContain(GERMANY);

    const firstName = document.querySelector<HTMLInputElement>(
      'input[placeholder="name, e.g. netherlands"]',
    );
    fireEvent.change(firstName as HTMLElement, { target: { value: 'germany' } });
    expect(offeredOn(1)).not.toContain(GERMANY);
    expect(offeredOn(0)).toContain(GERMANY);
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
