import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Tag } from 'antd';
import { CopyOutlined } from '@ant-design/icons';

import ConfigBlock from '@/components/clients/ConfigBlock';
import { clientNameInRemark, wireguardFileNames } from '@/lib/inbounds/label';
import {
  amneziawgConfigFromLink,
  isPostQuantumLink,
  wireguardConfigFromLink,
} from '@/lib/xray/inbound-link';
import { LinkTags, parseLinkParts } from '@/lib/xray/link-label';
import SubQrButton from './SubQrButton';

interface SubConfigsTabProps {
  links: string[];
  // Every client named on the page. A link's paired email is not always its owner's (a cisco
  // client's takes its place), so a file takes a name only from the link's own remark.
  clientNames: string[];
  onCopy: (value: string, toast?: string) => void;
}

export default function SubConfigsTab({ links, clientNames, onCopy }: SubConfigsTabProps) {
  const { t } = useTranslation();
  const rows = links.map((link, idx) => {
    const parts = parseLinkParts(link);
    const rowTitle = parts?.remark || `Link ${idx + 1}`;
    const isWireguardLink = link.startsWith('wireguard://') || link.startsWith('wg://');
    return {
      link,
      parts,
      rowTitle,
      wireguardConfig: isWireguardLink ? wireguardConfigFromLink(link, rowTitle) : '',
      amneziawgConfig: link.startsWith('vpn://') ? amneziawgConfigFromLink(link) : '',
    };
  });
  const tunnelRows = rows.filter((row) => row.wireguardConfig || row.amneziawgConfig);
  const fileNames = wireguardFileNames(
    tunnelRows.map((row) => row.rowTitle),
    tunnelRows.map((row) => clientNameInRemark(row.rowTitle, clientNames)),
  );
  const fileNameOf = new Map(tunnelRows.map((row, i) => [row, fileNames[i]]));

  return (
    <div className="sub-rows">
      <div className="sub-configs-bar">
        <Button
          icon={<CopyOutlined aria-hidden />}
          onClick={() => onCopy(links.join('\n'), t('subscription.copyAllConfigsCopied'))}
        >
          {t('subscription.copyAllConfigs')}
        </Button>
      </div>
      {rows.map((row) => {
        const { link, parts, rowTitle, wireguardConfig, amneziawgConfig } = row;
        const fileName = fileNameOf.get(row) ?? '';
        return (
          <Fragment key={link}>
            <div className="sub-row">
              {parts ? <LinkTags parts={parts} /> : <Tag className="sub-row-tag">LINK</Tag>}
              <span className="sub-row-title" dir="auto" title={rowTitle}>
                {rowTitle}
              </span>
              <div className="sub-row-actions">
                <Button
                  icon={<CopyOutlined aria-hidden />}
                  onClick={() => onCopy(link)}
                  aria-label={t('copy')}
                  title={t('copy')}
                />
                {!isPostQuantumLink(link) && (
                  <SubQrButton value={link} label={rowTitle} onCopy={onCopy} />
                )}
              </div>
            </div>
            {wireguardConfig && (
              <ConfigBlock
                label={t('pages.clients.wireguardConfig')}
                text={wireguardConfig}
                fileName={fileName}
                qrRemark={rowTitle}
                tagColor="cyan"
              />
            )}
            {amneziawgConfig && (
              <ConfigBlock
                label={t('pages.clients.amneziaWgConfig')}
                text={amneziawgConfig}
                fileName={fileName}
                qrRemark={rowTitle}
                tagColor="purple"
              />
            )}
          </Fragment>
        );
      })}
    </div>
  );
}
