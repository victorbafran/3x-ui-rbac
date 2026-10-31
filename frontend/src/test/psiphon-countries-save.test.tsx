import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';

import { keys } from '@/api/queryKeys';
import { HttpUtil } from '@/utils';
import PsiphonCard from '@/pages/relays/PsiphonCard';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

// The card and its hooks run for real; only fetch, under HttpUtil, is the panel.
const fetchMock = vi.fn<typeof fetch>();
vi.mocked(HttpUtil.get).mockRestore();
vi.mocked(HttpUtil.post).mockRestore();

const ENABLED = ['AT', 'CA', 'DE', 'ES', 'FI', 'FR', 'GB', 'NL', 'US'];
const IN_USE = ['AT', 'DE', 'FI', 'FR', 'GB', 'NL', 'US'];

interface ServerOptions {
  start?: string[];
  // Countries the panel refuses to switch off, though the card was never told.
  refuses?: string[];
  // While set, a save waits for land() so polls answer with the state before it.
  hold?: boolean;
}

// The panel as the owner had it: nine countries on, seven given to hosts, Canada and
// Spain switched on from the scan and used by nothing.
function makeServer({ start = ENABLED, refuses = IN_USE, hold = false }: ServerOptions = {}) {
  const server = { enabled: [...start], posts: [] as string[][] };
  const country = (code: string) => ({
    code,
    name: code,
    enabled: server.enabled.includes(code),
    running: server.enabled.includes(code),
    connected: server.enabled.includes(code),
    socksPort: 11181,
  });
  const state = () => ({
    supported: true,
    installed: true,
    regions: ENABLED,
    countries: ENABLED.map(country),
    warp: { mode: 'off', sites: '', registered: false },
    scan: {
      startedAt: 1793450000,
      finishedAt: 1793450600,
      total: ENABLED.length,
      results: ENABLED.map((code) => ({ code, connected: true, exitIp: '198.51.100.1', at: 1 })),
    },
  });
  let release: (() => void) | null = null;
  const reply = (body: unknown) =>
    new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  const ok = (obj: unknown, msg = '') => reply({ success: true, msg, obj });
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    if (url.endsWith('/csrf-token')) return ok('token');
    if (method === 'GET' && url.endsWith('/state')) return ok(state());
    if (method === 'GET' && url.endsWith('/inbounds')) return ok([]);
    if (method === 'POST' && url.endsWith('/countries')) {
      const { codes } = JSON.parse(String(init?.body)) as { codes: string[] };
      server.posts.push(codes);
      if (hold) {
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      const refused = refuses.find(
        (code) => server.enabled.includes(code) && !codes.includes(code),
      );
      if (refused) {
        return reply({
          success: false,
          msg: `Save Psiphon countries (${refused} is still the exit of in-443-tcp; take it off there first)`,
        });
      }
      server.enabled = [...codes];
      return ok(null, 'Save Psiphon countries');
    }
    return ok(null);
  });
  return {
    server,
    land: () => {
      release?.();
      release = null;
    },
  };
}

function box(card: HTMLElement, code: string): HTMLInputElement {
  return card.querySelector(`input[type="checkbox"][value="${code}"]`) as HTMLInputElement;
}

async function renderCard() {
  const queryClient = makeTestQueryClient();
  renderWithProviders(<PsiphonCard />, { queryClient });
  const card = (await screen.findByText('Save countries')).closest('.ant-card') as HTMLElement;
  await waitFor(() => expect(box(card, 'DE').checked).toBe(true));
  const save = within(card).getByText('Save countries').closest('button') as HTMLButtonElement;
  const poll = () =>
    act(() => queryClient.refetchQueries({ queryKey: keys.relays.psiphon.state() }));
  return { card, save, poll, queryClient };
}

const sorted = (codes: string[] | undefined) => [...(codes ?? [])].sort();

describe('saving the Psiphon countries from the card', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // The owner unticked Canada and Spain, saved, and found both ticked and running
  // after a refresh, while the same codes sent straight to the API switched them off.
  it('switches off the unticked countries while the state is being polled', async () => {
    const { server, land } = makeServer({ hold: true });
    const { card, save, poll } = await renderCard();

    fireEvent.click(box(card, 'CA'));
    fireEvent.click(box(card, 'ES'));
    await poll();
    expect(box(card, 'CA').checked).toBe(false);
    expect(box(card, 'ES').checked).toBe(false);

    fireEvent.click(save);
    await waitFor(() => expect(server.posts).toHaveLength(1));
    expect(sorted(server.posts[0])).toEqual(IN_USE);
    await poll();
    expect(box(card, 'CA').checked).toBe(false);

    await act(async () => land());
    await waitFor(() => expect(save.disabled).toBe(true));
    expect(server.enabled).toEqual(IN_USE);
    await poll();
    expect(box(card, 'CA').checked).toBe(false);
    expect(box(card, 'ES').checked).toBe(false);
    expect(box(card, 'DE').checked).toBe(true);
    expect(server.posts).toHaveLength(1);
  });

  // A refusal only ever showed as a toast, and the ticks stayed as the operator left
  // them, so the card looked saved until a refresh brought the countries back.
  it('says beside the button why a save was refused and shows what still runs', async () => {
    const { server } = makeServer({ refuses: [...IN_USE, 'CA'] });
    const { card, save } = await renderCard();

    fireEvent.click(box(card, 'CA'));
    fireEvent.click(box(card, 'ES'));
    fireEvent.click(save);
    await waitFor(() => expect(server.posts).toHaveLength(1));

    const alert = await within(card).findByRole('alert');
    expect(alert.textContent).toContain('CA is still the exit of in-443-tcp');
    expect(box(card, 'CA').checked).toBe(true);
    expect(box(card, 'ES').checked).toBe(true);
    expect(save.disabled).toBe(true);
    expect(server.enabled).toEqual(ENABLED);

    // The next change takes the old refusal away.
    fireEvent.click(box(card, 'ES'));
    expect(within(card).queryByRole('alert')).toBeNull();
  });
});
