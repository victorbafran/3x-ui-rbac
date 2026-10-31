import { useTranslation } from 'react-i18next';
import { Input, Radio } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { IP_FAMILIES, type IPFamily } from '@/schemas/relay';

// IPv6 served by this server itself, as a front's is: the family customers get
// and the address their IPv6 dials. Shared by the cisco and OpenVPN forms.
export default function SidecarIPv6Fields() {
  const { t } = useTranslation();
  const { control, getValues } = useFormContext();
  const family = (useWatch({ control, name: 'settings.ipFamily' }) ?? '') as IPFamily;

  return (
    <>
      <FormField
        name={['settings', 'ipFamily']}
        label={t('pages.inbounds.form.ipFamily')}
        tooltip={t('pages.inbounds.form.ipFamilyHint')}
      >
        <Radio.Group>
          {IP_FAMILIES.map((v) => (
            <Radio.Button key={v} value={v}>
              {v === 'both' ? t('pages.inbounds.form.ipFamilyBoth') : v === 'v6' ? 'IPv6' : 'IPv4'}
            </Radio.Button>
          ))}
        </Radio.Group>
      </FormField>
      {family ? (
        <FormField
          name={['settings', 'address6']}
          label={t('pages.inbounds.form.address6')}
          tooltip={t('pages.inbounds.form.address6Hint')}
          rules={{
            validate: (value) =>
              !getValues('settings.ipFamily') ||
              String(value ?? '').trim() !== '' ||
              t('pages.inbounds.form.address6Needed'),
          }}
        >
          <Input placeholder="ipv6.example.com" dir="ltr" />
        </FormField>
      ) : null}
    </>
  );
}
