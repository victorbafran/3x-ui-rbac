import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { Alert, Button } from 'antd';

import { useStandbyQuery } from '@/api/queries/useClones';

interface StandbyBannerProps {
  // The dashboard links to the copies tab; the tab itself does not.
  showLink?: boolean;
}

export default function StandbyBanner({ showLink = false }: StandbyBannerProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { data } = useStandbyQuery();
  if (!data?.standby) return null;
  return (
    <Alert
      type="warning"
      showIcon
      title={t('pages.standby.banner.title')}
      description={t('pages.standby.banner.desc')}
      action={
        showLink ? (
          <Button size="small" onClick={() => navigate('/settings#copies')}>
            {t('pages.standby.banner.open')}
          </Button>
        ) : undefined
      }
    />
  );
}
