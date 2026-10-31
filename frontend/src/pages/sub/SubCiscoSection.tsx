import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button, Tag } from 'antd';
import { AndroidOutlined, AppleOutlined, CopyOutlined, DownloadOutlined } from '@ant-design/icons';

import type { CiscoLogin } from './subPageModel';
import SubLoginSectionHead, { type LoginApp } from './SubLoginSectionHead';

// Where a customer gets a client that speaks AnyConnect. The V2Ray-family
// apps the rest of the subscription page recommends cannot talk to ocserv at
// all, so without these the page names two apps and leaves the customer to
// find them.
//
// Cisco's own app is the one to lead with: it is the reference client and is
// in both stores. OpenConnect is the open alternative, and the maintained
// Android build lives on F-Droid — the Play listing (app.openconnect) has not
// been updated since 2015.
const CLIENT_APPS: readonly LoginApp[] = [
  {
    key: 'cisco-android',
    icon: <AndroidOutlined aria-hidden />,
    labelKey: 'subscription.ciscoAppAndroid',
    url: 'https://play.google.com/store/apps/details?id=com.cisco.anyconnect.vpn.android.avf',
  },
  {
    key: 'cisco-ios',
    icon: <AppleOutlined aria-hidden />,
    labelKey: 'subscription.ciscoAppIos',
    url: 'https://apps.apple.com/app/id1135064690',
  },
  {
    key: 'openconnect-android',
    icon: <DownloadOutlined aria-hidden />,
    labelKey: 'subscription.ciscoAppOpenConnect',
    url: 'https://f-droid.org/packages/net.openconnect_vpn.android/',
  },
];

// AnyConnect has no share-link format, so a cisco client has nothing an app
// can import: the customer types the server in by hand, then the login below it.
export default function SubCiscoSection({
  logins,
  titled,
  guided,
  onCopy,
}: {
  logins: CiscoLogin[];
  titled: boolean;
  guided: boolean;
  onCopy: (value: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="sub-login-section">
      <SubLoginSectionHead
        kind="cisco"
        title={titled ? t('subscription.ciscoTitle') : ''}
        hint={guided ? t('subscription.ciscoHint') : ''}
        apps={guided ? CLIENT_APPS : []}
      />
      <div className="sub-rows">
        {logins.map((login, idx) => {
          const server = serverValue(login);
          return (
            <div className="sub-row sub-login-row" key={`${server}-${idx}`}>
              <Tag className="sub-row-tag">
                <bdi>{headingOf(login, t) || t('subscription.ciscoServer')}</bdi>
              </Tag>
              <code className="sub-login-value" dir="ltr">
                {server || '-'}
              </code>
              {server && (
                <div className="sub-row-actions">
                  <Button
                    icon={<CopyOutlined aria-hidden />}
                    aria-label={`${t('copy')}: ${server}`}
                    title={t('copy')}
                    onClick={() => onCopy(server)}
                  />
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// The panel's mark is English ("IPv6"); a pair it labels by family is named in the
// page's language instead, and any other label ("1", "2") is shown as sent.
function headingOf(login: CiscoLogin, t: TFunction): string {
  if (login.family === 'v6') return t('subscription.ciscoFamilyV6');
  if (login.family === 'v4') return t('subscription.ciscoFamilyV4');
  return login.label ?? '';
}

// An AnyConnect client dials 443 when no port is given, so an inbound on 443
// is typed as a bare hostname. Printing ":443" would have the customer type a
// port they do not need and make the address look more fragile than it is.
function serverValue(login: CiscoLogin): string {
  const raw = login.server ?? '';
  if (!raw) return '';
  // A bare IPv6 address is bracketed, or the port would read as one more group of it.
  const host = raw.includes(':') && !raw.startsWith('[') ? `[${raw}]` : raw;
  return login.port && login.port !== 443 ? `${host}:${login.port}` : host;
}
