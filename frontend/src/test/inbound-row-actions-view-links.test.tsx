import { describe, expect, it } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import type { QueryClient } from '@tanstack/react-query';

import { RowActionsCell } from '@/pages/inbounds/list/RowActions';
import type { DBInboundRecord } from '@/pages/inbounds/list/types';
import { renderWithProviders, resellerQueryClient } from './test-utils';

const record = { id: 7, protocol: 'vless', remark: 'unit' } as DBInboundRecord;

async function menuEntries(queryClient: QueryClient) {
  renderWithProviders(
    <RowActionsCell record={record} subEnable={false} hasClients onClick={() => {}} />,
    { queryClient },
  );
  fireEvent.click(screen.getByRole('button', { name: 'more' }));
  await screen.findAllByRole('menuitem');
  return screen.getAllByRole('menuitem').map((item) => item.textContent);
}

// Export fetches every client's links and keys, which the server refuses a caller
// without clients.viewLinks; offering it would end in an error and an empty export.
describe('the inbound row menu', () => {
  it('offers Export All URLs to a caller who may see links', async () => {
    expect(await menuEntries(resellerQueryClient())).toContain('Export All URLs');
  });

  it('leaves Export All URLs out for a caller who may not', async () => {
    const entries = await menuEntries(resellerQueryClient('clients.viewLinks'));
    expect(entries).toContain('Export Inbound');
    expect(entries).not.toContain('Export All URLs');
  });
});
