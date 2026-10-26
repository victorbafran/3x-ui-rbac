import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import { createDefaultHysteriaClient } from '@/lib/xray/inbound-defaults';
import { Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const { bulkCreate } = vi.hoisted(() => ({ bulkCreate: vi.fn() }));
vi.mock('@/hooks/useClients', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/hooks/useClients')>()),
  useClients: () => ({ bulkCreate }),
}));

// Xray reads a host's exit route out of bytes 6-7 of a UUID-shaped Hysteria auth;
// a v4 UUID's version nibble keeps a plain auth's route off every relay id's.
const ROUTABLE_AUTH = /^[0-9a-f]{12}4[0-9a-f]{19}$/;

function hysteriaAuthItem(): HTMLElement {
  const label = Array.from(document.querySelectorAll('.ant-form-item-label label')).find(
    (l) => (l.textContent ?? '').trim() === 'Hysteria Auth',
  );
  const item = label?.closest('.ant-form-item') as HTMLElement | null;
  if (!item) throw new Error('Hysteria Auth field not found');
  return item;
}

function hysteriaAuthInput(): HTMLInputElement {
  const input = hysteriaAuthItem().querySelector('input');
  if (!input) throw new Error('Hysteria Auth input not found');
  return input;
}

function renderClientForm() {
  renderWithProviders(
    <ClientFormModal
      open
      mode="add"
      client={null}
      inbounds={[{ id: 7, protocol: 'hysteria', tag: 'hy' }]}
      save={vi.fn().mockResolvedValue(null)}
      onOpenChange={() => {}}
    />,
  );
  const tab = Array.from(document.querySelectorAll('.ant-tabs-tab')).find(
    (t) => (t.textContent ?? '').trim() === 'Credentials',
  );
  if (!tab) throw new Error('Credentials tab not found');
  fireEvent.click(tab);
}

describe('Hysteria auth an exit route can ride', () => {
  it('gives a new client a 32-hex auth', async () => {
    renderClientForm();
    await waitFor(() => expect(hysteriaAuthInput().value).toMatch(ROUTABLE_AUTH));
  });

  it('regenerates a short auth into a 32-hex one', async () => {
    renderClientForm();
    const input = hysteriaAuthInput();
    fireEvent.change(input, { target: { value: 'shortauth' } });
    await waitFor(() => expect(hysteriaAuthInput().value).toBe('shortauth'));

    const regenerate = hysteriaAuthItem().querySelector('button');
    if (!regenerate) throw new Error('regenerate button not found');
    fireEvent.click(regenerate);
    await waitFor(() => expect(hysteriaAuthInput().value).toMatch(ROUTABLE_AUTH));
  });

  it('bulk-adds every client with a 32-hex auth', async () => {
    bulkCreate.mockResolvedValue(new Msg(true, '', { created: 2, skipped: [] }));
    renderWithProviders(
      <ClientBulkAddModal
        open
        inbounds={[{ id: 7, protocol: 'hysteria', tag: 'hy' }]}
        onOpenChange={() => {}}
      />,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Select all' }));
    const quantity = Array.from(document.querySelectorAll('.ant-form-item-label label'))
      .find((l) => (l.textContent ?? '').trim() === 'Number of Clients')
      ?.closest('.ant-form-item')
      ?.querySelector('input');
    if (!quantity) throw new Error('client count input not found');
    fireEvent.change(quantity, { target: { value: '2' } });

    const create = document.querySelector('.ant-modal-footer .ant-btn-primary');
    if (!create) throw new Error('Create button missing');
    fireEvent.click(create);

    await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
    const payloads = bulkCreate.mock.calls[0][0] as { client: { auth: string } }[];
    expect(payloads).toHaveLength(2);
    for (const { client } of payloads) expect(client.auth).toMatch(ROUTABLE_AUTH);
    expect(payloads[0].client.auth).not.toBe(payloads[1].client.auth);
  });

  // Random hex gave bytes 6-7 any value, so 1 auth in 65536 per country picked
  // that exit with no route asked for; 64 draws catch it, a lone draw rarely.
  it('seeds every default Hysteria client with a v4 UUID auth', () => {
    for (let i = 0; i < 64; i++) expect(createDefaultHysteriaClient().auth).toMatch(ROUTABLE_AUTH);
  });
});
