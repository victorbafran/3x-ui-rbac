import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { MinusOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, Button, Form, Input, InputNumber, Select, Space, Switch } from 'antd';
import { useFieldArray, useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useInboundOptions } from '@/api/queries/useInboundOptions';

// The countries this inbound offers. ocserv advertises them before it asks for
// a password, so the customer's own client shows a Group dropdown and picks
// where to come out — one address and one account, several exits.
//
// With no exit a country leaves through this server, which is free and right
// for the country the server is already in. With an exit it leaves through
// that interface, in practice a tunnel to a server standing in that country.
function LocationList() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const { fields, append, remove } = useFieldArray({ control, name: 'settings.locations' });
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
          <FormField name={['settings', 'locations', i, 'name']} noStyle>
            <Input placeholder={t('pages.inbounds.form.ciscoLocationName')} />
          </FormField>
          <FormField name={['settings', 'locations', i, 'label']} noStyle>
            <Input placeholder={t('pages.inbounds.form.ciscoLocationLabel')} />
          </FormField>
          <FormField name={['settings', 'locations', i, 'exit']} noStyle>
            <Input placeholder={t('pages.inbounds.form.ciscoLocationExit')} />
          </FormField>
          <Button aria-label={t('remove')} size="small" onClick={() => remove(i)}>
            <MinusOutlined />
          </Button>
        </Space.Compact>
      ))}
    </Form.Item>
  );
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
