import type { ComponentProps } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';

import HostFormModal from '@/pages/hosts/HostFormModal';
import type { HostRecord } from '@/api/queries/useHostsQuery';
import { ThemeProvider } from '@/hooks/useTheme';
import { makeTestQueryClient } from './test-utils';

type Save = ComponentProps<typeof HostFormModal>['save'];

// A refusal names the clients whose Hysteria auth cannot carry a route, too long
// to read in a passing toast; the form has to keep it on screen.
const REFUSAL =
  'a Hysteria client carries its exit country in its auth, which must be a UUID or 32 hex characters; on hy: regenerate the auth of alice, bob, carol, dave, erin and 3 more';

const hyHost: HostRecord = {
  groupId: 'g1',
  remark: 'Hysteria DE',
  inboundIds: [7],
  hosts: ['hy.example.com'],
  exit: 'wgr1',
};
const otherHost: HostRecord = { ...hyHost, groupId: 'g2', remark: 'Hysteria UK', exit: '' };

function refusingSave() {
  return vi.fn<Save>().mockResolvedValue({ success: false, msg: REFUSAL });
}

function renderForm(save: Save) {
  const qc = makeTestQueryClient();
  const tree = (open: boolean, host: HostRecord) => (
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <HostFormModal
          open={open}
          mode="edit"
          host={host}
          inboundOptions={[{ id: 7, protocol: 'hysteria', tag: 'hy' }]}
          existingHosts={[hyHost, otherHost]}
          save={save}
          onOpenChange={() => {}}
        />
      </QueryClientProvider>
    </ThemeProvider>
  );
  const view = render(tree(true, hyHost));
  return {
    reopenWith(host: HostRecord) {
      view.rerender(tree(false, hyHost));
      view.rerender(tree(true, host));
    },
  };
}

function modalErrors(): string[] {
  return Array.from(document.querySelectorAll('.ant-modal-body .ant-alert-error')).map(
    (a) => a.textContent ?? '',
  );
}

function clickSave() {
  const ok = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!ok) throw new Error('save button missing');
  fireEvent.click(ok);
}

describe('Host form save refusal', () => {
  it('keeps the backend refusal inside the form', async () => {
    const save = refusingSave();
    renderForm(save);
    clickSave();
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(modalErrors()).toContain(REFUSAL));
  });

  it('opens the next host without the previous refusal', async () => {
    const { reopenWith } = renderForm(refusingSave());
    clickSave();
    await waitFor(() => expect(modalErrors()).toContain(REFUSAL));

    reopenWith(otherHost);
    await waitFor(() =>
      expect(
        (document.querySelector('.ant-modal-body input') as HTMLInputElement | null)?.value,
      ).toBe('Hysteria UK'),
    );
    expect(modalErrors()).toEqual([]);
  });
});
