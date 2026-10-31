import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, AutoComplete, Input, InputNumber, Select, Switch } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useRelayOptions } from '@/api/queries/useRelaysQuery';
import type { RelayOption } from '@/schemas/relay';

// An OpenVPN inbound is one country. The protocol has no way to offer a
// choice before connecting — that is a cisco feature, built for corporate
// networks with departments — so a customer who has several countries holds
// several profiles and switches between them in their client.

export default function OpenVPNFields() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  // A dotted path, not an array: an array of names asks for several values
  // and comes back as a tuple, which is never the boolean this reads as.
  const useTcp = useWatch({ control, name: 'settings.useTcp' }) as boolean | undefined;

  // The exit servers already enrolled, offered by the country the panel found
  // them in. A reseller who cannot read the relay routes simply gets no
  // suggestions and can still type an interface of their own.
  const { options: relays } = useRelayOptions();
  // A Psiphon country is an Xray TUN named after its exit, so OpenVPN routes into it
  // like a relay's tunnel; the countries follow the exit servers under their own heading.
  const exitOptions = useMemo(() => {
    const pending = (r: RelayOption) => (r.ready ? '' : ' (…)');
    const servers = relays
      .filter((r) => r.kind !== 'psiphon')
      .map((r) => ({
        value: r.interface,
        label: r.interface
          ? `${r.label || r.name} — ${r.address}${pending(r)}`
          : `${r.label || r.name} — ${t('pages.inbounds.form.openvpnExitThisServer')}`,
      }));
    const psiphon = relays
      .filter((r) => r.kind === 'psiphon')
      .map((r) => ({ value: r.interface, label: `${r.label || r.name}${pending(r)}` }));
    return psiphon.length ? [...servers, { label: 'Psiphon', options: psiphon }] : servers;
  }, [relays, t]);

  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={t('pages.inbounds.form.openvpnNeedsBinary')}
        description={t('pages.inbounds.form.openvpnNeedsBinaryHint')}
      />
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={t('pages.inbounds.form.openvpnOneCountry')}
        description={t('pages.inbounds.form.openvpnOneCountryHint')}
      />

      <FormField
        name={['settings', 'exit']}
        label={t('pages.inbounds.form.openvpnExit')}
        tooltip={t('pages.inbounds.form.openvpnExitHint')}
      >
        <AutoComplete
          options={exitOptions}
          placeholder={t('pages.inbounds.form.openvpnExitThisServer')}
          filterOption={(input, option) =>
            String(option && 'value' in option ? option.value : '')
              .toLowerCase()
              .includes(input.toLowerCase()) ||
            String(option?.label ?? '')
              .toLowerCase()
              .includes(input.toLowerCase())
          }
        />
      </FormField>

      <FormField
        name={['settings', 'useTcp']}
        label={t('pages.inbounds.form.openvpnUseTcp')}
        tooltip={t('pages.inbounds.form.openvpnUseTcpHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {useTcp && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.inbounds.form.openvpnTcpWarning')}
          description={t('pages.inbounds.form.openvpnTcpWarningHint')}
        />
      )}

      <FormField
        name={['settings', 'network']}
        label={t('pages.inbounds.form.openvpnNetwork')}
        tooltip={t('pages.inbounds.form.openvpnNetworkHint')}
      >
        <Input placeholder="10.1.0.0" />
      </FormField>
      <FormField name={['settings', 'netmask']} label={t('pages.inbounds.form.openvpnNetmask')}>
        <Input placeholder="255.255.255.0" />
      </FormField>
      <FormField
        name={['settings', 'dns']}
        label={t('pages.inbounds.form.openvpnDns')}
        tooltip={t('pages.inbounds.form.openvpnDnsHint')}
      >
        <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="1.1.1.1" />
      </FormField>
      <FormField
        name={['settings', 'maxClients']}
        label={t('pages.inbounds.form.openvpnMaxClients')}
        tooltip={t('pages.inbounds.form.openvpnMaxClientsHint')}
      >
        <InputNumber min={0} max={100000} placeholder="1024" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'maxSameClients']}
        label={t('pages.inbounds.form.openvpnMaxSameClients')}
        tooltip={t('pages.inbounds.form.openvpnMaxSameClientsHint')}
      >
        <InputNumber min={0} max={100} placeholder="2" style={{ width: '100%' }} />
      </FormField>
    </>
  );
}
