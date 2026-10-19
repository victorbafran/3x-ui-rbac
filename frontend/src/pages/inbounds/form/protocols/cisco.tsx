import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { MinusOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, AutoComplete, Button, Form, Input, InputNumber, Select, Space, Switch } from 'antd';
import { useFieldArray, useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useInboundOptions } from '@/api/queries/useInboundOptions';
import { useRelayOptions } from '@/api/queries/useRelaysQuery';
import type { RelayOption } from '@/schemas/relay';

// The countries this inbound offers. ocserv advertises them before it asks for
// a password, so the customer's own client shows a Group dropdown and picks
// where to come out — one address and one account, several exits.
//
// With no exit a country leaves through this server, which is free and right
// for the country the server is already in. With an exit it leaves through
// that interface, in practice a tunnel to a server standing in that country.
function LocationList() {
  const { t } = useTranslation();
  const { control, setValue } = useFormContext();
  const { fields, append, remove } = useFieldArray({ control, name: 'settings.locations' });
  const rows = useWatch({ control, name: 'settings.locations' }) as
    | { name?: string; label?: string; exit?: string }[]
    | undefined;
  // Relays the panel has already enrolled, offered by the name the operator
  // gave them rather than by an interface nobody would remember. Still an
  // AutoComplete rather than a Select: an operator with a tunnel of their own
  // can type its interface, and a reseller who cannot read the relay routes
  // simply gets no suggestions instead of an empty, broken dropdown.
  const { options: relays } = useRelayOptions();
  const exitOptions = relays.map((r) => ({
    value: r.interface,
    label: `${r.label || r.name} — ${r.address}${r.ready ? '' : ' (…)'}`,
  }));

  // A country that leaves through an enrolled exit takes that server's own
  // country and flag, and the two name boxes are hidden for it: the panel
  // already asked the server where it was, so asking the operator to type it
  // again is two more things to get wrong. A country with no exit leaves
  // through this server, has nothing to be named after, and keeps its boxes.
  const relayFor = (iface: unknown) =>
    typeof iface === 'string' && iface ? relays.find((r) => r.interface === iface) : undefined;

  const nameFromRelay = (relay: RelayOption) => ({
    name: relay.country.toLowerCase().replace(/[^a-z0-9]/g, '') || relay.name,
    label: [flagOf(relay.countryCode), relay.country].filter(Boolean).join(' ') || relay.label,
  });

  const fillFromRelay = (index: number, iface: string) => {
    const relay = relayFor(iface);
    if (!relay) return;
    const { name, label } = nameFromRelay(relay);
    // Overwritten rather than filled: the boxes are not shown for a country
    // backed by a server, so there is nothing of the operator's to protect,
    // and switching to another country has to rename it.
    if (name) setValue(`settings.locations.${index}.name`, name, { shouldDirty: true });
    if (label) setValue(`settings.locations.${index}.label`, label, { shouldDirty: true });
  };
  return (
    <Form.Item
      label={t('pages.inbounds.form.ciscoLocations')}
      tooltip={t('pages.inbounds.form.ciscoLocationsHint')}
    >
      <Button
        aria-label={t('add')}
        size="small"
        onClick={() => append({ name: '', label: '', exit: '' })}
      >
        <PlusOutlined />
      </Button>
      {fields.map((field, i) => (
        <Space.Compact key={field.id} block className="mt-4">
          {relayFor(rows?.[i]?.exit) ? (
            <Input
              readOnly
              value={rows?.[i]?.label || rows?.[i]?.name || ''}
              style={{ width: '45%' }}
            />
          ) : (
            <>
              <FormField name={['settings', 'locations', i, 'name']} noStyle>
                <Input placeholder={t('pages.inbounds.form.ciscoLocationName')} />
              </FormField>
              <FormField name={['settings', 'locations', i, 'label']} noStyle>
                <Input placeholder={t('pages.inbounds.form.ciscoLocationLabel')} />
              </FormField>
            </>
          )}
          <FormField name={['settings', 'locations', i, 'exit']} noStyle>
            <AutoComplete
              options={exitOptions}
              onSelect={(value: string) => fillFromRelay(i, value)}
              placeholder={t('pages.inbounds.form.ciscoLocationExit')}
              filterOption={(input, option) =>
                String(option?.value ?? '')
                  .toLowerCase()
                  .includes(input.toLowerCase()) ||
                String(option?.label ?? '')
                  .toLowerCase()
                  .includes(input.toLowerCase())
              }
            />
          </FormField>
          <Button aria-label={t('remove')} size="small" onClick={() => remove(i)}>
            <MinusOutlined />
          </Button>
        </Space.Compact>
      ))}
    </Form.Item>
  );
}

function flagOf(code: string): string {
  const up = code.trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(up)) return '';
  return String.fromCodePoint(...[...up].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

export default function CiscoFields() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const port = useWatch({ control, name: 'port' }) as number | undefined;
  const { data: inboundOptions } = useInboundOptions();

  // ocserv owns TCP and UDP on its port outright. REALITY's whole cover story
  // is that port 443 answers like an ordinary TLS site, so the two cannot
  // share one. This warns rather than refuses: the operator may be moving
  // REALITY off this host, and a panel that blocks the save cannot be argued
  // with.
  const realityInbound = useMemo(() => {
    for (const option of inboundOptions ?? []) {
      if (option.nodeId == null && option.security === 'reality') return option;
    }
    return undefined;
  }, [inboundOptions]);

  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('pages.inbounds.form.ciscoNeedsOcserv')}
        description={t('pages.inbounds.form.ciscoNeedsOcservHint')}
      />
      {realityInbound && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('pages.inbounds.form.ciscoRealityWarning')}
          description={t('pages.inbounds.form.ciscoRealityWarningHint', {
            remark: realityInbound.remark || realityInbound.tag || realityInbound.id,
            port: realityInbound.port ?? 443,
          })}
        />
      )}
      {port === 443 && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('pages.inbounds.form.ciscoPort443')}
          description={t('pages.inbounds.form.ciscoPort443Hint')}
        />
      )}

      <FormField
        name={['settings', 'network']}
        label={t('pages.inbounds.form.ciscoNetwork')}
        tooltip={t('pages.inbounds.form.ciscoNetworkHint')}
      >
        <Input placeholder="10.8.0.0" />
      </FormField>
      <FormField name={['settings', 'netmask']} label={t('pages.inbounds.form.ciscoNetmask')}>
        <Input placeholder="255.255.255.0" />
      </FormField>
      <FormField
        name={['settings', 'dns']}
        label={t('pages.inbounds.form.ciscoDns')}
        tooltip={t('pages.inbounds.form.ciscoDnsHint')}
      >
        <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="1.1.1.1" />
      </FormField>
      <FormField
        name={['settings', 'udpPort']}
        label={t('pages.inbounds.form.ciscoUdpPort')}
        tooltip={t('pages.inbounds.form.ciscoUdpPortHint')}
      >
        <InputNumber
          min={0}
          max={65535}
          placeholder={String(port ?? 443)}
          style={{ width: '100%' }}
        />
      </FormField>
      <FormField
        name={['settings', 'disableUdp']}
        label={t('pages.inbounds.form.ciscoDisableUdp')}
        tooltip={t('pages.inbounds.form.ciscoDisableUdpHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      <FormField
        name={['settings', 'maxClients']}
        label={t('pages.inbounds.form.ciscoMaxClients')}
        tooltip={t('pages.inbounds.form.ciscoMaxClientsHint')}
      >
        <InputNumber min={0} max={100000} placeholder="1024" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'maxSameClients']}
        label={t('pages.inbounds.form.ciscoMaxSameClients')}
        tooltip={t('pages.inbounds.form.ciscoMaxSameClientsHint')}
      >
        <InputNumber min={0} max={100} placeholder="2" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'certFile']}
        label={t('pages.inbounds.form.ciscoCertFile')}
        tooltip={t('pages.inbounds.form.ciscoCertFileHint')}
      >
        <Input placeholder="/etc/ssl/certs/vpn.pem" />
      </FormField>
      <FormField name={['settings', 'keyFile']} label={t('pages.inbounds.form.ciscoKeyFile')}>
        <Input placeholder="/etc/ssl/private/vpn.key" />
      </FormField>
      <LocationList />
    </>
  );
}
