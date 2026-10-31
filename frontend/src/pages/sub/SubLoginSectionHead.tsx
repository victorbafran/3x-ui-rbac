import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from 'antd';
import { CloudServerOutlined, FileProtectOutlined } from '@ant-design/icons';

export interface LoginApp {
  key: string;
  icon: ReactNode;
  labelKey: string;
  url: string;
}

// A protocol's name, how it is set up and where to get its app. The name is left out
// when the card's own title already says it; hint and apps appear once per card.
export default function SubLoginSectionHead({
  kind,
  title,
  hint,
  apps,
}: {
  kind: 'cisco' | 'openvpn';
  title: string;
  hint: string;
  apps: readonly LoginApp[];
}) {
  const { t } = useTranslation();
  if (!title && !hint && apps.length === 0) return null;
  return (
    <div className="sub-login-head">
      {title && (
        <div className="sub-login-title">
          <span className="sub-login-mark" aria-hidden>
            {kind === 'cisco' ? (
              <CloudServerOutlined aria-hidden />
            ) : (
              <FileProtectOutlined aria-hidden />
            )}
          </span>
          {title}
        </div>
      )}
      {hint && <p className="sub-muted sub-login-hint">{hint}</p>}
      {apps.length > 0 && (
        <div className="sub-login-apps">
          {apps.map((app) => (
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
        </div>
      )}
    </div>
  );
}
