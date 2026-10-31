import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button, Card, Space, Tooltip, Typography } from 'antd';
import { AndroidOutlined, AppleOutlined, CopyOutlined, DownloadOutlined } from '@ant-design/icons';

import { ClipboardManager } from '@/utils';

export interface CiscoLogin {
  email?: string;
  server?: string;
  port?: number;
  username?: string;
  password?: string;
  // Tells a pair's IPv6 and IPv4 logins apart, as their config names are.
  label?: string;
  // "v6" or "v4" when the label names the family, which the page then says itself.
  family?: string;
}

// Where a customer gets a client that speaks AnyConnect. The V2Ray-family
// apps the rest of the subscription page recommends cannot talk to ocserv at
// all, so without these the page names two apps and leaves the customer to
// find them.
//
// Cisco's own app is the one to lead with: it is the reference client and is
// in both stores. OpenConnect is the open alternative, and the maintained
// Android build lives on F-Droid — the Play listing (app.openconnect) has not
// been updated since 2015.
const CLIENT_APPS = [
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
] as const;

// AnyConnect has no share-link format, so a cisco client has nothing an app
// can import — the customer types these four values in by hand. Without this
// the subscription page is the right link showing nothing, which reads as a
// broken link rather than as a protocol that works differently.
export default function SubCiscoCard({ logins }: { logins: CiscoLogin[] }) {
  const { t } = useTranslation();
  if (!logins.length) return null;

  const copy = (value?: string) => {
    if (value) void ClipboardManager.copyText(value);
  };

  return (
    <Card className="sub-card-inner" size="small" title={t('subscription.ciscoTitle')}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        {t('subscription.ciscoHint')}
      </Typography.Paragraph>

      <Space wrap size={8} style={{ marginBottom: 16 }}>
        {CLIENT_APPS.map((app) => (
          <Button
            key={app.key}
            size="small"
            icon={app.icon}
            href={app.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t(app.labelKey)}
          </Button>
        ))}
      </Space>

      {logins.map((login, idx) => (
        <div className="sub-cisco-login" key={`${login.email ?? 'cisco'}-${idx}`}>
          {headingOf(login, t) ? (
            <Typography.Text strong>
              <bdi>{headingOf(login, t)}</bdi>
            </Typography.Text>
          ) : null}
          <dl className="sub-cisco-rows">
            <Row label={t('subscription.ciscoServer')} value={serverValue(login)} onCopy={copy} />
            <Row label={t('subscription.ciscoUsername')} value={login.username} onCopy={copy} />
            <Row label={t('subscription.ciscoPassword')} value={login.password} onCopy={copy} />
          </dl>
        </div>
      ))}
    </Card>
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

function Row({
  label,
  value,
  onCopy,
}: {
  label: string;
  value?: string;
  onCopy: (v?: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="sub-cisco-row">
      <dt>{label}</dt>
      <dd>
        <code>{value || '-'}</code>
        {value && (
          <Tooltip title={t('copy')}>
            <Button
              size="small"
              type="text"
              icon={<CopyOutlined aria-hidden />}
              aria-label={t('copy')}
              onClick={() => onCopy(value)}
            />
          </Tooltip>
        )}
      </dd>
    </div>
  );
}
