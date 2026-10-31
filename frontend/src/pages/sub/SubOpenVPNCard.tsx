import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Card, Space, Tooltip, Typography } from 'antd';
import {
  AndroidOutlined,
  AppleOutlined,
  CopyOutlined,
  DesktopOutlined,
  DownloadOutlined,
} from '@ant-design/icons';

import { ClipboardManager } from '@/utils';
import { getMessage } from '@/utils/messageBus';

export interface OpenVPNProfile {
  email?: string;
  label?: string;
  fileName?: string;
  url?: string;
  username?: string;
  password?: string;
}

// OpenVPN's own client is the one to lead with. Unlike the V2Ray-family apps
// the rest of this page recommends, nothing else on a phone opens a .ovpn
// file reliably, so a customer who is not told where to get it downloads a
// file their phone has no idea what to do with.
const CLIENT_APPS = [
  {
    key: 'openvpn-android',
    icon: <AndroidOutlined aria-hidden />,
    labelKey: 'subscription.openvpnAppAndroid',
    url: 'https://play.google.com/store/apps/details?id=net.openvpn.openvpn',
  },
  {
    key: 'openvpn-ios',
    icon: <AppleOutlined aria-hidden />,
    labelKey: 'subscription.openvpnAppIos',
    url: 'https://apps.apple.com/app/id590379981',
  },
  {
    key: 'openvpn-desktop',
    icon: <DesktopOutlined aria-hidden />,
    labelKey: 'subscription.openvpnAppDesktop',
    url: 'https://openvpn.net/client/',
  },
] as const;

// Why the server has no file, as it answers; only an inbound that has not
// started yet is worth trying again in a minute.
const REASON_KEYS: Record<string, string> = {
  notStarted: 'subscription.openvpnNotReady',
  notInstalled: 'subscription.openvpnNotInstalled',
  noAddress: 'subscription.openvpnNoAddress',
  invalid: 'subscription.openvpnInvalid',
};

async function failureKey(response: Response): Promise<string> {
  try {
    const body: unknown = await response.json();
    const reason = (body as { reason?: unknown } | null)?.reason;
    if (typeof reason === 'string' && Object.hasOwn(REASON_KEYS, reason))
      return REASON_KEYS[reason];
  } catch {
    // An older panel or a proxy answered with no reason; the generic text stays.
  }
  return 'subscription.openvpnNotReady';
}

// An OpenVPN client imports a file, not a link, so there is nothing for the
// subscription URL to carry. The file is fetched on click rather than built
// into this page: it embeds the server's shared key, and a page a customer
// opens to check their traffic should not leave that in every browser cache
// it passes through.
export default function SubOpenVPNCard({ profiles }: { profiles: OpenVPNProfile[] }) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState('');
  if (!profiles.length) return null;

  const copy = (value?: string) => {
    if (value) void ClipboardManager.copyText(value);
  };

  const download = async (profile: OpenVPNProfile) => {
    const url = profile.url;
    if (!url || busy) return;
    setBusy(url);
    try {
      const response = await fetch(url, { credentials: 'same-origin' });
      if (!response.ok) {
        getMessage().error(t(await failureKey(response)));
        return;
      }
      const blob = await response.blob();
      const href = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = href;
      anchor.download = profile.fileName || 'vpn.ovpn';
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      // Revoking straight away cancels the download in Safari, which has not
      // read the blob yet when click() returns.
      window.setTimeout(() => URL.revokeObjectURL(href), 1000);
    } catch {
      // The request itself failed; saying so beats a download that silently does not happen.
      getMessage().error(t('subscription.openvpnNotReady'));
    } finally {
      setBusy('');
    }
  };

  return (
    <Card className="sub-card-inner" size="small" title={t('subscription.openvpnTitle')}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        {t('subscription.openvpnHint')}
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

      {profiles.map((profile, idx) => (
        <dl className="sub-cisco-rows" key={`${profile.url ?? profile.label ?? 'openvpn'}-${idx}`}>
          <div className="sub-cisco-row">
            <dt>{profile.label || t('subscription.openvpnProfile')}</dt>
            <dd>
              <Button
                size="small"
                type="primary"
                icon={<DownloadOutlined aria-hidden />}
                loading={busy === profile.url}
                disabled={!profile.url}
                onClick={() => void download(profile)}
              >
                {t('subscription.openvpnDownload')}
              </Button>
            </dd>
          </div>
          <Row label={t('subscription.openvpnUsername')} value={profile.username} onCopy={copy} />
          <Row label={t('subscription.openvpnPassword')} value={profile.password} onCopy={copy} />
        </dl>
      ))}
    </Card>
  );
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
