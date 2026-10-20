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
    icon: <AndroidOutlined />,
    labelKey: 'subscription.openvpnAppAndroid',
    url: 'https://play.google.com/store/apps/details?id=net.openvpn.openvpn',
  },
  {
    key: 'openvpn-ios',
    icon: <AppleOutlined />,
    labelKey: 'subscription.openvpnAppIos',
    url: 'https://apps.apple.com/app/id590379981',
  },
  {
    key: 'openvpn-desktop',
    icon: <DesktopOutlined />,
    labelKey: 'subscription.openvpnAppDesktop',
    url: 'https://openvpn.net/client/',
  },
] as const;

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
      if (!response.ok) throw new Error(String(response.status));
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
      // The one failure a customer can actually hit: the inbound has never
      // started, so the server has no certificate to put in the file yet.
      // Saying so beats a download that silently does not happen.
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
                icon={<DownloadOutlined />}
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
              icon={<CopyOutlined />}
              aria-label={t('copy')}
              onClick={() => onCopy(value)}
            />
          </Tooltip>
        )}
      </dd>
    </div>
  );
}
