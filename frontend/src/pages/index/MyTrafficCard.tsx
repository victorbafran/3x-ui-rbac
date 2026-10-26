import { useTranslation } from 'react-i18next';
import { Card, Progress, Typography, theme } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';

import { SizeFormatter } from '@/utils';
import type { AdminMe } from '@/api/queries/useMeQuery';

interface MyTrafficCardProps {
  me: AdminMe;
  isMobile: boolean;
}

// The host's throughput belongs to the whole server. An admin who is not a
// super_admin sees the traffic of the customers they own, against their quota.
// The figures come from /admins/me, which counts the same clients the quota
// job counts -- summing their inbounds instead would include every other
// reseller sharing one.
export default function MyTrafficCard({ me, isMobile }: MyTrafficCardProps) {
  const { t } = useTranslation();
  const { token } = theme.useToken();

  const up = me.usedUp ?? 0;
  const down = me.usedDown ?? 0;
  const used = up + down;
  const quota = me.trafficQuota ?? 0;
  const percent = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  const remaining = quota > 0 ? Math.max(0, quota - used) : 0;

  return (
    <Card hoverable styles={{ body: { padding: 0 } }}>
      <div className="ov-wide-head">
        <div>
          <div className="ov-kicker">{t('pages.index.myTraffic.title')}</div>
          <div className="ov-sub">{`${t('pages.index.myTraffic.clients')} · ${me.clientCount ?? 0}`}</div>
        </div>
        <div className="ov-wide-legend">
          <div className="ov-legend-label">
            <ArrowUpOutlined aria-hidden style={{ color: token.colorPrimary }} />
            {t('pages.index.myTraffic.upload')}
            <span className="ov-legend-num">{SizeFormatter.sizeFormat(up)}</span>
          </div>
          <div className="ov-legend-label">
            <ArrowDownOutlined aria-hidden style={{ color: token.colorTextTertiary }} />
            {t('pages.index.myTraffic.download')}
            <span className="ov-legend-num">{SizeFormatter.sizeFormat(down)}</span>
          </div>
        </div>
      </div>

      <div className="ov-wide-chart" style={{ padding: isMobile ? '16px' : '24px' }}>
        {quota > 0 ? (
          <Progress
            percent={percent}
            showInfo
            status={percent >= 100 ? 'exception' : 'normal'}
            strokeColor={token.colorPrimary}
          />
        ) : (
          <Typography.Text type="secondary">{t('pages.index.myTraffic.unlimited')}</Typography.Text>
        )}
      </div>

      <div className="ov-wide-foot">
        <div>
          <div className="ov-kicker">{t('pages.index.myTraffic.used')}</div>
          <div className="ov-foot-value">{SizeFormatter.sizeFormat(used)}</div>
        </div>
        <span className="ov-foot-sep" />
        <div>
          <div className="ov-kicker">
            {quota > 0 ? t('pages.index.myTraffic.remaining') : t('pages.index.myTraffic.quota')}
          </div>
          <div className="ov-foot-value">
            {quota > 0 ? SizeFormatter.sizeFormat(remaining) : t('pages.index.myTraffic.unlimited')}
          </div>
        </div>
        <span className="ov-foot-sep" />
        <div>
          <div className="ov-kicker">{t('pages.index.myTraffic.expires')}</div>
          <div className="ov-foot-value">
            {me.expireAt ? dayjs(me.expireAt).format('YYYY-MM-DD') : t('pages.admins.never')}
          </div>
        </div>
      </div>
    </Card>
  );
}
