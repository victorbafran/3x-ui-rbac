import { useTranslation } from 'react-i18next';
import { useFormContext, useWatch } from 'react-hook-form';

import { CertIssueControl } from '@/components/CertIssueControl';
import { useMeQuery } from '@/api/queries/useMeQuery';

// Fetches a Let's Encrypt certificate for the names customers type and fills
// the inbound's two certificate fields with where it landed. The panel keeps
// it renewed, and the VPN server picks the renewed one up on its own.
//
// Shown to the super admin only, because the route behind it is theirs: it
// opens port 80 on this server and writes under the panel's own directory.
// Hidden for an inbound served by a node too, since the certificate would be
// issued here while the node reads its files from its own disk.
export function CertIssueButton({ certField, keyField }: { certField: string; keyField: string }) {
  const { t } = useTranslation();
  const { me } = useMeQuery();
  const { control, setValue } = useFormContext();
  const nodeId = useWatch({ control, name: 'nodeId' }) as number | null | undefined;

  if (me?.role !== 'super_admin' || typeof nodeId === 'number') return null;

  return (
    <CertIssueControl
      explain={t('pages.inbounds.certIssue.explain')}
      explainHint={t('pages.inbounds.certIssue.explainHint')}
      namesHint={t('pages.inbounds.certIssue.namesHint')}
      onIssued={(cert) => {
        setValue(certField, cert.certFile, { shouldDirty: true });
        setValue(keyField, cert.keyFile, { shouldDirty: true });
      }}
    />
  );
}
