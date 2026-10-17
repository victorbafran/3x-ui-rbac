import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Input, InputNumber, Select } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useInboundOptions } from '@/api/queries/useInboundOptions';

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
    </>
  );
}
