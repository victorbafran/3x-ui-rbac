import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Card } from 'antd';
import {
  CopyOutlined,
  KeyOutlined,
  SafetyCertificateOutlined,
  UserOutlined,
} from '@ant-design/icons';

import SubCiscoSection from './SubCiscoSection';
import SubOpenVPNSection from './SubOpenVPNSection';
import { groupLoginsByCredentials, type CiscoLogin, type OpenVPNProfile } from './subPageModel';

// Cisco and OpenVPN both sign in with a name and password typed by hand, and one
// client's pair opens both. Each pair is shown once, under every row it opens.
export default function SubLoginsCard({
  logins,
  profiles,
  onCopy,
}: {
  logins: CiscoLogin[];
  profiles: OpenVPNProfile[];
  onCopy: (value: string) => void;
}) {
  const { t } = useTranslation();
  const groups = groupLoginsByCredentials(logins, profiles);
  if (groups.length === 0) return null;

  const bothProtocols = logins.length > 0 && profiles.length > 0;
  const title = bothProtocols
    ? t('subscription.loginsTitleBoth')
    : t(logins.length > 0 ? 'subscription.ciscoTitle' : 'subscription.openvpnTitle');
  const firstCisco = groups.findIndex((g) => g.logins.length > 0);
  const firstOpenVPN = groups.findIndex((g) => g.profiles.length > 0);

  return (
    <Card className="sub-card-inner sub-logins" size="small" title={title}>
      {groups.map((group, idx) => (
        <section className="sub-login-group" key={`${group.username}-${idx}`}>
          {group.logins.length > 0 && (
            <SubCiscoSection
              logins={group.logins}
              titled={bothProtocols}
              guided={idx === firstCisco}
              onCopy={onCopy}
            />
          )}
          {group.profiles.length > 0 && (
            <SubOpenVPNSection
              profiles={group.profiles}
              titled={bothProtocols}
              guided={idx === firstOpenVPN}
            />
          )}
          <div className="sub-login-creds">
            <Credential
              icon={<UserOutlined aria-hidden />}
              label={t('subscription.loginUsername')}
              value={group.username}
              onCopy={onCopy}
            />
            <Credential
              icon={<KeyOutlined aria-hidden />}
              label={t('subscription.loginPassword')}
              value={group.password}
              onCopy={onCopy}
            />
            {group.logins.length > 0 && group.profiles.length > 0 && (
              <p className="sub-login-note">
                <SafetyCertificateOutlined aria-hidden />
                {t('subscription.loginSharedNote')}
              </p>
            )}
          </div>
        </section>
      ))}
    </Card>
  );
}

function Credential({
  icon,
  label,
  value,
  onCopy,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  onCopy: (value: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="sub-login-cred">
      <span className="sub-login-cred-label">
        {icon}
        {label}
      </span>
      <div className="sub-login-cred-line">
        <code className="sub-login-cred-value" dir="ltr">
          {value || '-'}
        </code>
        {value && (
          <Button
            icon={<CopyOutlined aria-hidden />}
            aria-label={`${t('copy')}: ${label}`}
            title={t('copy')}
            onClick={() => onCopy(value)}
          />
        )}
      </div>
    </div>
  );
}
