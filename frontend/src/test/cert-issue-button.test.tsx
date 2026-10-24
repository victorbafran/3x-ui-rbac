import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useEffect } from 'react';
import { act, cleanup, fireEvent, screen } from '@testing-library/react';
import { FormProvider, useForm, type UseFormReturn } from 'react-hook-form';

import { HttpUtil } from '@/utils';
import { renderWithProviders } from './test-utils';

let role = 'super_admin';
vi.mock('@/api/queries/useMeQuery', () => ({
  useMeQuery: () => ({ me: { role }, loading: false, can: () => true }),
}));

const { CertIssueButton } = await import('@/pages/inbounds/form/CertIssueButton');

type Values = { nodeId: number | null; settings: { certFile: string; keyFile: string } };
let form: UseFormReturn<Values> | null = null;

function Harness({ nodeId = null }: { nodeId?: number | null }) {
  const methods = useForm<Values>({
    defaultValues: { nodeId, settings: { certFile: '', keyFile: '' } },
  });
  useEffect(() => {
    form = methods;
  }, [methods]);
  return (
    <FormProvider {...methods}>
      <CertIssueButton certField="settings.certFile" keyField="settings.keyFile" />
    </FormProvider>
  );
}

async function askFor(name: string) {
  fireEvent.click(screen.getByText('Get a certificate'));
  await act(async () => {});
  const input = document.querySelector('.ant-modal input') as HTMLInputElement;
  // A trailing separator turns the typed text into a name, the way pasting
  // "a.example, b.example" does.
  fireEvent.change(input, { target: { value: `${name},` } });
  await act(async () => {});
  fireEvent.click(screen.getByText('Get the certificate'));
  await act(async () => {});
}

describe('the certificate button on a cisco inbound', () => {
  beforeEach(() => {
    role = 'super_admin';
    form = null;
  });
  afterEach(() => {
    vi.restoreAllMocks();
    cleanup();
  });

  // A name typed and then the dialog closed is still there when it reopens.
  it('keeps the names typed across closing the dialog', async () => {
    renderWithProviders(<Harness />);
    fireEvent.click(screen.getByText('Get a certificate'));
    await act(async () => {});
    const input = document.querySelector('.ant-modal input') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'tun.example.ir,' } });
    await act(async () => {});
    fireEvent.click(screen.getByText('Cancel'));
    await act(async () => {});
    fireEvent.click(screen.getByText('Get a certificate'));
    await act(async () => {});
    expect(document.querySelector('.ant-modal')?.textContent).toContain('tun.example.ir');
  });

  // The whole point: the operator types the name customers use and the two
  // fields fill themselves, so nobody has to know where certificates live.
  it('fills both fields with the certificate it got', async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue({
      success: true,
      msg: '',
      obj: {
        certFile: '/etc/x-ui/certs/tun.example.ir/fullchain.pem',
        keyFile: '/etc/x-ui/certs/tun.example.ir/privkey.pem',
        names: ['tun.example.ir'],
        notAfter: '2027-01-19T13:31:06Z',
        reused: false,
      },
    } as never);
    renderWithProviders(<Harness />);
    await askFor('tun.example.ir');

    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/issueCert',
      { names: ['tun.example.ir'] },
      expect.anything(),
    );
    expect(form?.getValues('settings.certFile')).toBe(
      '/etc/x-ui/certs/tun.example.ir/fullchain.pem',
    );
    expect(form?.getValues('settings.keyFile')).toBe('/etc/x-ui/certs/tun.example.ir/privkey.pem');
  });

  // The server says which name failed and what to change. A generic failure
  // would throw exactly that away.
  it('shows the server’s own reason when it cannot get one', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue({
      success: false,
      msg: 'Let’s Encrypt could not fetch the proof for tun.example.ir from port 80',
    } as never);
    renderWithProviders(<Harness />);
    await askFor('tun.example.ir');

    expect(document.body.textContent).toContain(
      'Let’s Encrypt could not fetch the proof for tun.example.ir from port 80',
    );
    expect(form?.getValues('settings.certFile')).toBe('');
  });

  // The route behind it is the super admin's: it opens port 80 and writes
  // under the panel's own directory. A reseller editing an inbound must not
  // be shown a button that can only fail.
  it('is not offered to a reseller', () => {
    role = 'admin';
    renderWithProviders(<Harness />);
    expect(screen.queryByText('Get a certificate')).toBeNull();
  });

  // A node serves the inbound from its own disk, where a certificate issued
  // on this panel does not exist.
  it('is not offered for an inbound served by a node', () => {
    renderWithProviders(<Harness nodeId={2} />);
    expect(screen.queryByText('Get a certificate')).toBeNull();
  });
});
