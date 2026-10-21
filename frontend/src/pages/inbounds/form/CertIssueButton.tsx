import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { Alert, Button, Form, Modal, Select, message } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { HttpUtil } from '@/utils';
import { useMeQuery } from '@/api/queries/useMeQuery';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

interface Issued {
  certFile: string;
  keyFile: string;
  names: string[];
  notAfter: string;
  reused: boolean;
}

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
  const [open, setOpen] = useState(false);
  const [names, setNames] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [messageApi, contextHolder] = message.useMessage();

  if (me?.role !== 'super_admin' || typeof nodeId === 'number') return null;

  const issue = async () => {
    setBusy(true);
    setError('');
    try {
      const msg = await HttpUtil.post<Issued>(
        '/panel/api/server/issueCert',
        { names },
        { ...JSON_HEADERS, silent: true },
      );
      if (!msg?.success || !msg.obj) {
        // The server's own words: they say which name failed and what to
        // change, which a generic failure would throw away.
        setError(msg?.msg || t('somethingWentWrong'));
        return;
      }
      const obj = msg.obj;
      setValue(certField, obj.certFile, { shouldDirty: true });
      setValue(keyField, obj.keyFile, { shouldDirty: true });
      const vars = {
        names: obj.names.join(', '),
        date: new Date(obj.notAfter).toLocaleDateString(),
      };
      messageApi.success(
        obj.reused
          ? t('pages.inbounds.certIssue.reused', vars)
          : t('pages.inbounds.certIssue.done', vars),
      );
      setOpen(false);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      {contextHolder}
      <Button
        icon={<SafetyCertificateOutlined />}
        onClick={() => {
          setError('');
          setOpen(true);
        }}
        style={{ marginBottom: 16 }}
      >
        {t('pages.inbounds.certIssue.button')}
      </Button>
      <Modal
        open={open}
        title={t('pages.inbounds.certIssue.title')}
        okText={t('pages.inbounds.certIssue.issue')}
        onOk={issue}
        okButtonProps={{ disabled: names.length === 0 }}
        confirmLoading={busy}
        onCancel={() => setOpen(false)}
        destroyOnHidden
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          title={t('pages.inbounds.certIssue.explain')}
          description={t('pages.inbounds.certIssue.explainHint')}
        />
        <Form layout="vertical">
          <Form.Item
            label={t('pages.inbounds.certIssue.names')}
            tooltip={t('pages.inbounds.certIssue.namesHint')}
          >
            <Select
              mode="tags"
              value={names}
              onChange={(v: string[]) => setNames(v.map((s) => s.trim()).filter(Boolean))}
              tokenSeparators={[',', ' ']}
              open={false}
              placeholder="vpn.example.com"
            />
          </Form.Item>
        </Form>
        {error ? <Alert type="error" showIcon title={error} /> : null}
      </Modal>
    </>
  );
}
