import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Card, Progress, Typography, theme } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';

import { HttpUtil, SizeFormatter } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { SlimInboundListSchema } from '@/schemas/inbound';
import { keys } from '@/api/queryKeys';
import type { AdminMe } from '@/api/queries/useMeQuery';

// Same key, schema and shape as the inbounds page, so both read one cache entry.
async function fetchSlimInbounds(): Promise<unknown[]> {
  const msg = await HttpUtil.get('/panel/api/inbounds/list/slim', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch inbounds');
  const validated = parseMsg(msg, SlimInboundListSchema, 'inbounds/list/slim');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

interface MyTrafficCardProps {
  me: AdminMe;
  isMobile: boolean;
}

// The host's throughput belongs to the whole server. An admin who is not a
// super_admin sees the traffic of the inbounds they own, against their quota.
export default function MyTrafficCard({ me, isMobile }: MyTrafficCardProps) {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const { data } = useQuery({
    queryKey: keys.inbounds.slim(),
    queryFn: fetchSlimInbounds,
    staleTime: Infinity,
  });

  const totals = useMemo(() => {
    let up = 0;
    let down = 0;
    for (const ib of data ?? []) {
      const rec = ib as { up?: number; down?: number };
      up += rec.up || 0;
      down += rec.down || 0;
    }
    return { up, down, count: (data ?? []).length };
  }, [data]);

  const used = totals.up + totals.down;
  const quota = me.trafficQuota ?? 0;
  const percent = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  const remaining = quota > 0 ? Math.max(0, quota - used) : 0;

  return (
    <Card hoverable styles={{ body: { padding: 0 } }}>
      <div className="ov-wide-head">
        <div>
          <div className="ov-kicker">{t('pages.index.myTraffic.title')}</div>
          <div className="ov-sub">{`${t('pages.index.myTraffic.inbounds')} · ${totals.count}`}</div>
        </div>
        <div className="ov-wide-legend">
          <div className="ov-legend-label">
            <ArrowUpOutlined style={{ color: token.colorPrimary }} />
            {t('pages.index.myTraffic.upload')}
            <span className="ov-legend-num">{SizeFormatter.sizeFormat(totals.up)}</span>
          </div>
          <div className="ov-legend-label">
            <ArrowDownOutlined style={{ color: token.colorTextTertiary }} />
            {t('pages.index.myTraffic.download')}
            <span className="ov-legend-num">{SizeFormatter.sizeFormat(totals.down)}</span>
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
