import type { ReactElement } from 'react';
import { render, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import { ThemeProvider } from '@/hooks/useTheme';
import { keys } from '@/api/queryKeys';
import type { AdminMe } from '@/generated/zod';
import { queryClient as appQueryClient } from '@/queryClient';

export function makeTestQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// The app's own cache rules, under which a result stays fresh for a while.
export function appCacheQueryClient() {
  return new QueryClient({ defaultOptions: appQueryClient.getDefaultOptions() });
}

const RESELLER_PERMISSIONS = [
  'sections.inbounds',
  'sections.clients',
  'clients.create',
  'clients.edit',
  'clients.resetTraffic',
  'clients.viewLinks',
];

// Signed in as a reseller, with the switches named turned off by their super_admin.
export function resellerQueryClient(...switchedOff: string[]) {
  const queryClient = makeTestQueryClient();
  const me: AdminMe = {
    id: 2,
    username: 'reseller',
    role: 'reseller',
    permissions: Object.fromEntries(
      RESELLER_PERMISSIONS.map((key) => [key, !switchedOff.includes(key)]),
    ),
    clientCount: 0,
    expireAt: 0,
    trafficQuota: 0,
    usedUp: 0,
    usedDown: 0,
    usersLimit: 0,
  };
  queryClient.setQueryData(keys.admins.me(), me);
  return queryClient;
}

export function renderWithProviders(ui: ReactElement, options?: { queryClient?: QueryClient }) {
  const queryClient = options?.queryClient ?? makeTestQueryClient();
  return render(
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>{ui}</ThemeProvider>
    </QueryClientProvider>,
  );
}

export function fieldLabels(): string[] {
  return Array.from(document.querySelectorAll('.ant-form-item-label label'))
    .map((el) => (el.textContent ?? '').trim())
    .filter(Boolean);
}

function selectRootForField(fieldId: string): HTMLElement {
  const control = document.getElementById(fieldId);
  const select = control?.closest('.ant-select') as HTMLElement | null;
  if (!select) throw new Error(`Select not found for field id: ${fieldId}`);
  return select;
}

function openSelect(select: HTMLElement) {
  const target = (select.querySelector('.ant-select-selector') ?? select) as HTMLElement;
  fireEvent.mouseDown(target);
}

function openDropdownOptions(): string[] {
  return Array.from(
    document.querySelectorAll(
      '.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option',
    ),
  )
    .map((o) => (o.getAttribute('title') ?? o.textContent ?? '').trim())
    .filter(Boolean);
}

export function listSelectOptions(fieldId: string): string[] {
  const select = selectRootForField(fieldId);
  openSelect(select);
  const opts = openDropdownOptions();
  fireEvent.keyDown(select, { key: 'Escape' });
  return opts;
}

export function chooseSelectOption(fieldId: string, optionText: string) {
  const select = selectRootForField(fieldId);
  openSelect(select);
  const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
    (o) => (o.getAttribute('title') ?? o.textContent ?? '').trim() === optionText,
  );
  if (!option) throw new Error(`Option '${optionText}' not found for field '${fieldId}'`);
  fireEvent.click(option);
}
