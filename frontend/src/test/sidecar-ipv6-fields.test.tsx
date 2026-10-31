import { afterEach, describe, expect, it, vi } from 'vitest';
import { Form } from 'antd';
import { useEffect, type ReactNode } from 'react';
import { FormProvider, useForm, type UseFormReturn } from 'react-hook-form';
import { act, cleanup, fireEvent, screen } from '@testing-library/react';

import { HttpUtil } from '@/utils';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import { renderWithProviders } from './test-utils';

vi.mock('@/api/queries/useMeQuery', () => ({
  useMeQuery: () => ({ me: { role: 'super_admin' }, loading: false, can: () => true }),
}));
vi.mock('@/api/queries/useInboundOptions', () => ({ useInboundOptions: () => ({ data: [] }) }));
vi.mock('@/api/queries/useRelaysQuery', () => ({
  useRelayOptions: () => ({ options: [] }),
  useRelaysQuery: () => ({ relays: [], isFetched: true, isLoading: false, error: null }),
}));

const { default: CiscoFields } = await import('@/pages/inbounds/form/protocols/cisco');
const { default: OpenVPNFields } = await import('@/pages/inbounds/form/protocols/openvpn');

let form: UseFormReturn<InboundFormValues> | null = null;

function Harness({ values, children }: { values: Record<string, unknown>; children: ReactNode }) {
  const methods = useForm<InboundFormValues>({ defaultValues: values as never });
  useEffect(() => {
    form = methods;
  }, [methods]);
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

const ciscoValues = (settings: Record<string, unknown>, top: Record<string, unknown> = {}) => ({
  nodeId: null,
  port: 443,
  ...top,
  settings: { certFile: '', keyFile: '', locations: [], ipFamily: '', address6: '', ...settings },
});

// certCovers answers with uncovered; issueCert succeeds. Returns the spy.
function serverSays(uncovered: string[], host = 'vpn.example.com') {
  return vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) => {
    if (url === '/panel/api/server/certCovers') {
      return { success: true, msg: '', obj: { host, uncovered } } as never;
    }
    return { success: false, msg: 'not in this test' } as never;
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  cleanup();
  form = null;
});

describe('an OpenVPN or cisco inbound’s own IPv6', () => {
  // IPv4 is what every inbound had; the address box only matters once IPv6 is on,
  // and saving IPv6 with nothing for customers to dial is refused before the server sees it.
  it('asks for the IPv6 address once IPv6 is picked, and will not save without one', async () => {
    renderWithProviders(
      <Harness values={{ settings: { exit: '', ipFamily: '', address6: '' } }}>
        <OpenVPNFields />
      </Harness>,
    );
    expect(screen.queryByText('IPv6 domain or address')).toBeNull();

    fireEvent.click(screen.getByText('IPv6'));
    await act(async () => {});
    expect(form?.getValues('settings.ipFamily' as never)).toBe('v6');
    expect(screen.getByText('IPv6 domain or address')).toBeTruthy();

    let valid = true;
    await act(async () => {
      valid = (await form?.trigger()) ?? true;
    });
    expect(valid).toBe(false);
    expect(document.body.textContent).toContain(
      'Type this server’s IPv6 address or a domain for it.',
    );

    fireEvent.click(screen.getByText('Both'));
    fireEvent.change(screen.getByPlaceholderText('ipv6.example.com'), {
      target: { value: 'ipv6.example.com' },
    });
    await act(async () => {
      valid = (await form?.trigger()) ?? false;
    });
    expect(valid).toBe(true);
    expect(form?.getValues('settings.ipFamily' as never)).toBe('both');
  });

  // AnyConnect refuses a certificate without the name it dials: an IPv6 name the
  // cisco certificate lacks would leave every IPv6 customer outside.
  it('warns when the cisco certificate does not cover the IPv6 name', async () => {
    const post = serverSays(['ipv6.example.com']);
    renderWithProviders(
      <Harness
        values={ciscoValues({
          certFile: '/etc/x-ui/certs/vpn/fullchain.pem',
          ipFamily: 'both',
          address6: 'ipv6.example.com',
        })}
      >
        <CiscoFields />
      </Harness>,
    );
    expect(await screen.findByText('The certificate does not cover ipv6.example.com')).toBeTruthy();
    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/certCovers',
      { certFile: '/etc/x-ui/certs/vpn/fullchain.pem', names: ['ipv6.example.com'] },
      expect.anything(),
    );
  });

  it('says nothing when the certificate covers it, or IPv6 is off', async () => {
    const post = serverSays([]);
    renderWithProviders(
      <Harness
        values={ciscoValues({
          certFile: '/etc/x-ui/certs/vpn/fullchain.pem',
          ipFamily: 'v6',
          address6: 'ipv6.example.com',
        })}
      >
        <CiscoFields />
      </Harness>,
    );
    await act(async () => {});
    expect(post).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/does not cover/)).toBeNull();
    cleanup();

    post.mockClear();
    renderWithProviders(
      <Harness
        values={ciscoValues({
          certFile: '/etc/x-ui/certs/vpn/fullchain.pem',
          address6: 'ipv6.example.com',
        })}
      >
        <CiscoFields />
      </Harness>,
    );
    await act(async () => {});
    expect(post).not.toHaveBeenCalled();
  });

  // One certificate has to carry both names AnyConnect may dial: the name IPv4
  // customers are given and the IPv6 one, so the button asks for the two together.
  it('asks the certificate button for the IPv4 name and the IPv6 one', async () => {
    serverSays([]);
    renderWithProviders(
      <Harness
        values={ciscoValues(
          { ipFamily: 'both', address6: 'ipv6.example.com' },
          { shareAddrStrategy: 'custom', shareAddr: 'vpn.example.com' },
        )}
      >
        <CiscoFields />
      </Harness>,
    );
    fireEvent.click(screen.getByText('Get a certificate'));
    await act(async () => {});
    const names = Array.from(
      document.querySelectorAll('.ant-modal .ant-select-selection-item'),
    ).map((el) => el.textContent);
    expect(names).toEqual(['vpn.example.com', 'ipv6.example.com']);
  });

  it('leaves the certificate names to the operator while customers stay on IPv4', async () => {
    renderWithProviders(
      <Harness
        values={ciscoValues(
          { address6: 'ipv6.example.com' },
          { shareAddrStrategy: 'custom', shareAddr: 'vpn.example.com' },
        )}
      >
        <CiscoFields />
      </Harness>,
    );
    fireEvent.click(screen.getByText('Get a certificate'));
    await act(async () => {});
    expect(document.querySelectorAll('.ant-modal .ant-select-selection-item')).toHaveLength(0);
  });
});
