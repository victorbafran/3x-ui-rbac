import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from 'antd';
import {
  AndroidOutlined,
  AppleOutlined,
  DesktopOutlined,
  DownloadOutlined,
} from '@ant-design/icons';

import { getMessage } from '@/utils/messageBus';
import type { OpenVPNProfile } from './subPageModel';
import SubLoginSectionHead, { type LoginApp } from './SubLoginSectionHead';

// OpenVPN's own client is the one to lead with. Unlike the V2Ray-family apps
// the rest of this page recommends, nothing else on a phone opens a .ovpn
// file reliably, so a customer who is not told where to get it downloads a
// file their phone has no idea what to do with.
const CLIENT_APPS: readonly LoginApp[] = [
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
];

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
export default function SubOpenVPNSection({
  profiles,
  titled,
  guided,
}: {
  profiles: OpenVPNProfile[];
  titled: boolean;
  guided: boolean;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState('');

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
    <div className="sub-login-section">
      <SubLoginSectionHead
        kind="openvpn"
        title={titled ? t('subscription.openvpnTitle') : ''}
        hint={guided ? t('subscription.openvpnHint') : ''}
        apps={guided ? CLIENT_APPS : []}
      />
      <div className="sub-rows">
        {profiles.map((profile, idx) => (
          <div
            className="sub-row sub-login-row"
            key={`${profile.url ?? profile.label ?? 'openvpn'}-${idx}`}
          >
            <span className="sub-login-file" dir="auto">
              {profile.label || t('subscription.openvpnProfile')}
            </span>
            <div className="sub-row-actions">
              <Button
                type="primary"
                icon={<DownloadOutlined aria-hidden />}
                loading={busy === profile.url}
                disabled={!profile.url}
                onClick={() => void download(profile)}
              >
                {t('subscription.openvpnDownload')}
              </Button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
