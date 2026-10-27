import { describe, expect, it, vi } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';

import HostFormModal from '@/pages/hosts/HostFormModal';
import type { HostRecord } from '@/api/queries/useHostsQuery';
import { ThemeProvider } from '@/hooks/useTheme';
import { makeTestQueryClient } from './test-utils';

const hyHost: HostRecord = {
  groupId: 'g1',
  remark: 'Hysteria DE',
  inboundIds: [7],
  hosts: ['hy.example.com'],
  exit: 'wgr1',
};

function remark() {
  return (document.querySelector('.ant-modal-body input') as HTMLInputElement | null)?.value;
}

describe('Host form reopen', () => {
  // The page keeps one mounted modal; Add opens it with host null, the same key
  // the closed modal held, so the edited host's values stayed until a refresh.
  it('opens Add empty after editing a host', async () => {
    const qc = makeTestQueryClient();
    const tree = (open: boolean, mode: 'add' | 'edit', host: HostRecord | null) => (
      <ThemeProvider>
        <QueryClientProvider client={qc}>
          <HostFormModal
            open={open}
            mode={mode}
            host={host}
            inboundOptions={[{ id: 7, protocol: 'hysteria', tag: 'hy' }]}
            existingHosts={[hyHost]}
            save={vi.fn()}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>
    );
    const view = render(tree(true, 'edit', hyHost));
    await waitFor(() => expect(remark()).toBe('Hysteria DE'));

    view.rerender(tree(false, 'edit', hyHost));
    view.rerender(tree(true, 'add', null));
    await waitFor(() => expect(remark()).toBe(''));
  });
});
