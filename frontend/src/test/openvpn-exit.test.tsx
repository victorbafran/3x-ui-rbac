import { describe, expect, it, vi } from 'vitest';
import { Form } from 'antd';
import type { ReactNode } from 'react';
import { FormProvider, useForm } from 'react-hook-form';
import { cleanup, fireEvent } from '@testing-library/react';

import OpenVPNFields from '@/pages/inbounds/form/protocols/openvpn';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import { renderWithProviders } from './test-utils';

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
}));

function Harness({ children }: { children: ReactNode }) {
  const methods = useForm<InboundFormValues>({
    defaultValues: { settings: { exit: '' } } as never,
  });
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

describe('the exit an OpenVPN inbound leaves through', () => {
  // A Psiphon country is an Xray TUN named after its exit, which OpenVPN routes into
  // like a relay: offered after the exit servers, and saved as that exit.
  it('offers the Psiphon countries under their own heading', () => {
    renderWithProviders(
      <Harness>
        <OpenVPNFields />
      </Harness>,
    );
    const exitInput = document.querySelector<HTMLInputElement>('input') as HTMLInputElement;
    fireEvent.mouseDown(exitInput);
    fireEvent.focus(exitInput);

    const listed = Array.from(
      document.querySelectorAll('.ant-select-item-group, .ant-select-item-option'),
    ).map((el) => el.textContent ?? '');
    expect(listed.slice(-2)).toEqual(['Psiphon', '🇦🇹 Austria']);
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      (el.textContent ?? '').includes('Austria'),
    );
    fireEvent.click(option as HTMLElement);
    expect(exitInput.value).toBe('psi-AT');
    cleanup();
  });
});
