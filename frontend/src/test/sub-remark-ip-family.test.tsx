import { useState } from 'react';
import { fireEvent, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { AllSetting } from '@/models/setting';
import { AllSettingSchema } from '@/schemas/setting';
import SubscriptionGeneralTab from '@/pages/settings/SubscriptionGeneralTab';
import { chooseSelectOption, renderWithProviders } from './test-utils';

function Harness({ initial }: { initial?: unknown }) {
  const [allSetting, setAllSetting] = useState(() => new AllSetting(initial));
  return (
    <>
      <SubscriptionGeneralTab
        allSetting={allSetting}
        updateSetting={(patch) =>
          setAllSetting((current) => new AllSetting({ ...current, ...patch }))
        }
      />
      <output data-testid="stored">{allSetting.subRemarkIPFamily}</output>
    </>
  );
}

function exampleNames(): string[] {
  const line = document.querySelector('[data-testid="ip-family-example"]');
  return Array.from(line?.querySelectorAll('bdi') ?? []).map((n) => n.textContent ?? '');
}

// The two configs a front set to "both" hands out share one name; this choice
// is the only thing that tells them apart, and the example is what the operator reads.
describe('telling a front’s two configs apart', () => {
  it('shows today’s IPv6 / IPv4 marks by default and sends each choice', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/settings#subscription']}>
        <Harness />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('tab', { name: /Information/ }));
    expect(
      screen.getByRole('combobox', { name: 'Tell an entry server’s two configs apart' }),
    ).toBeTruthy();
    expect(exampleNames()).toEqual(['🇩🇪 Germany · IPv6', '🇩🇪 Germany · IPv4']);

    chooseSelectOption('sub-remark-ip-family', '1 / 2');
    expect(screen.getByTestId('stored').textContent).toBe('number');
    expect(exampleNames()).toEqual(['🇩🇪 Germany 1', '🇩🇪 Germany 2']);

    chooseSelectOption('sub-remark-ip-family', 'Nothing added');
    expect(screen.getByTestId('stored').textContent).toBe('off');
    expect(exampleNames()).toEqual(['🇩🇪 Germany', '🇩🇪 Germany']);

    chooseSelectOption('sub-remark-ip-family', '· IPv6 / · IPv4');
    expect(screen.getByTestId('stored').textContent).toBe('version');
  });

  // The backend refuses anything else; the page has to stop it before saving.
  it('lets only the three choices through to a save', () => {
    for (const v of ['version', 'number', 'off']) {
      expect(AllSettingSchema.safeParse({ subRemarkIPFamily: v }).success, v).toBe(true);
    }
    for (const v of ['', 'v6', 'Number', 1]) {
      expect(AllSettingSchema.safeParse({ subRemarkIPFamily: v }).success, String(v)).toBe(false);
    }
  });
});
