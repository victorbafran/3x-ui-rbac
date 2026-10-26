import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Card, ConfigProvider, Layout, Tabs, message } from 'antd';
import type { TabsProps } from 'antd';
import {
  AppstoreOutlined,
  ClockCircleOutlined,
  CustomerServiceOutlined,
  LinkOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons';

import { ClipboardManager, LanguageManager } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { useTheme } from '@/hooks/useTheme';
import SubAppsTab from './SubAppsTab';
import SubCiscoCard, { type CiscoLogin } from './SubCiscoCard';
import SubConfigsTab from './SubConfigsTab';
import SubHeader from './SubHeader';
import SubHero from './SubHero';
import SubLinksTab from './SubLinksTab';
import SubOpenVPNCard, { type OpenVPNProfile } from './SubOpenVPNCard';
import { buildSubApps, daysUntil, detectPlatform, resolveSubStatus } from './subPageModel';
import './SubPage.css';

const subData = window.__SUB_PAGE_DATA__ || {};

const sId = subData.sId || '';
const subUrl = subData.subUrl || '';
const subJsonUrl = subData.subJsonUrl || '';
const subClashUrl = subData.subClashUrl || '';
const subTitle = subData.subTitle || '';
const subSupportUrl = subData.subSupportUrl || '';
const updateHours = Number(subData.subUpdates || 0);
const announce = subData.announce || '';
const links: string[] = Array.isArray(subData.links) ? subData.links : [];
const linkEmails: string[] = Array.isArray(subData.emails) ? subData.emails : [];
const ciscoLogins: CiscoLogin[] = Array.isArray(subData.ciscoLogins) ? subData.ciscoLogins : [];
const openvpnProfiles: OpenVPNProfile[] = Array.isArray(subData.openvpnProfiles)
  ? subData.openvpnProfiles
  : [];
// Neither cisco nor OpenVPN produces a link an app can import, so a customer
// who has only those two has a subscription made entirely of the cards above.
const nothingToImport =
  links.length === 0 && (ciscoLogins.length > 0 || openvpnProfiles.length > 0);
const totalByte = Number(subData.totalByte || 0);
const usedByte =
  Number(subData.usedByte || 0) ||
  Number(subData.downloadByte || 0) + Number(subData.uploadByte || 0);
const expireMs = Number(subData.expire || 0) * 1000;
const clientEmail = [
  ...new Set(
    [
      ...linkEmails,
      ...ciscoLogins.map((l) => l.email ?? ''),
      ...openvpnProfiles.map((p) => p.email ?? ''),
    ].filter(Boolean),
  ),
].join(', ');
const loadedAt = Date.now();

const heroData = {
  status: resolveSubStatus({ enabled: !!subData.enabled, usedByte, totalByte, expireMs }, loadedAt),
  daysLeft: daysUntil(expireMs, loadedAt),
  usedByte,
  totalByte,
  expireMs,
  lastOnlineMs: Number(subData.lastOnline || 0),
  download: subData.download || '0',
  upload: subData.upload || '0',
  used: subData.used || '0',
  total: subData.total || '∞',
  remained: subData.remained || '',
  datepicker: subData.datepicker || 'gregorian',
};

const apps = buildSubApps({ subUrl, sId, subTitle });
const initialPlatform = detectPlatform(navigator.userAgent);
const RTL_LANGUAGES = new Set(['fa-IR', 'ar-EG']);

export default function SubPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);
  const [lang, setLang] = useState<string>(() => LanguageManager.getLanguage('subscription'));

  const onLangChange = useCallback((next: string) => {
    setLang(next);
    LanguageManager.setLanguage(next, 'subscription');
  }, []);

  const copy = useCallback(
    async (value: string, toast?: string) => {
      if (!value) return;
      const ok = await ClipboardManager.copyText(value);
      if (ok) messageApi.success(toast ?? t('copied'));
    },
    [t, messageApi],
  );

  const open = useCallback((url: string) => {
    if (url) window.open(url, '_blank');
  }, []);

  const tabs = useMemo(() => {
    const items: NonNullable<TabsProps['items']> = [];
    // A subscription holding nothing but cisco and OpenVPN clients has no
    // importable link: the subscription tab would offer a URL that adds
    // nothing, the QR invites a scan no such client can make, and the apps
    // tab recommends V2Ray-family apps that cannot speak either protocol.
    // The cards above are the whole subscription in that case.
    if (nothingToImport) {
      return items;
    }
    if (subUrl || subJsonUrl || subClashUrl) {
      items.push({
        key: 'subscription',
        icon: <LinkOutlined aria-hidden />,
        label: t('subscription.tabLinks'),
        children: (
          <SubLinksTab
            subUrl={subUrl}
            subJsonUrl={subJsonUrl}
            subClashUrl={subClashUrl}
            onCopy={copy}
          />
        ),
      });
    }
    if (subUrl) {
      items.push({
        key: 'apps',
        icon: <AppstoreOutlined aria-hidden />,
        label: t('subscription.tabApps'),
        children: <SubAppsTab apps={apps} initialPlatform={initialPlatform} onOpen={open} />,
      });
    }
    if (links.length > 0) {
      items.push({
        key: 'configs',
        icon: <UnorderedListOutlined aria-hidden />,
        label: (
          <>
            {t('subscription.tabConfigs')}
            <span className="sub-tab-count">{links.length}</span>
          </>
        ),
        children: <SubConfigsTab links={links} onCopy={copy} />,
      });
    }
    return items;
  }, [t, copy, open]);

  const direction = RTL_LANGUAGES.has(lang) ? 'rtl' : 'ltr';
  const pageClass = ['subscription-page', isDark && 'is-dark', isUltra && 'is-ultra']
    .filter(Boolean)
    .join(' ');

  return (
    <ConfigProvider theme={antdThemeConfig} direction={direction}>
      {messageContextHolder}
      <Layout className={pageClass} dir={direction}>
        <Layout.Content className="sub-content">
          <Card className="sub-card">
            <SubHeader
              title={subTitle}
              sId={sId}
              email={clientEmail}
              lang={lang}
              onLangChange={onLangChange}
            />
            {announce && <Alert type="info" showIcon title={announce} className="sub-announce" />}
            <SubHero {...heroData} lang={lang} />
            <SubCiscoCard logins={ciscoLogins} />
            <SubOpenVPNCard profiles={openvpnProfiles} />
            {tabs.length > 0 && <Tabs className="sub-tabs" tabBarGutter={24} items={tabs} />}
            {((updateHours > 0 && !nothingToImport) || subSupportUrl) && (
              <footer className="sub-footer">
                {/* Nothing re-fetches a cisco or OpenVPN subscription: the
                    credentials are typed in once and the file is imported
                    once, so an update interval would promise a refresh that
                    never has anything to refresh. */}
                {updateHours > 0 && !nothingToImport && (
                  <span>
                    <ClockCircleOutlined aria-hidden />
                    {t('subscription.updateInterval', { hours: updateHours })}
                  </span>
                )}
                {subSupportUrl && (
                  <a href={subSupportUrl} target="_blank" rel="noopener noreferrer">
                    <CustomerServiceOutlined aria-hidden />
                    {t('subscription.support')}
                  </a>
                )}
              </footer>
            )}
          </Card>
        </Layout.Content>
      </Layout>
    </ConfigProvider>
  );
}
