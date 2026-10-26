import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { Alert, Button, Form, Modal, Select, message } from 'antd';

import { HttpUtil } from '@/utils';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

export interface IssuedCert {
  certFile: string;
  keyFile: string;
  names: string[];
  notAfter: string;
  reused: boolean;
}

// Fetches a Let's Encrypt certificate for the names typed and hands back where
// it landed; each screen decides which fields that fills and what it says.
export function CertIssueControl({
  initialNames = [],
  onIssued,
  explain,
  explainHint,
  namesHint,
  doneNote,
}: {
  initialNames?: string[];
  onIssued: (cert: IssuedCert) => void;
  explain: string;
  explainHint: string;
  namesHint: string;
  doneNote?: string;
}) {
  const { t } = useTranslation();
  const uid = useId();
  const [open, setOpen] = useState(false);
  const [names, setNames] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [messageApi, contextHolder] = message.useMessage();

  const issue = async () => {
    setBusy(true);
    setError('');
    try {
      const msg = await HttpUtil.post<IssuedCert>(
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
      onIssued(obj);
      const vars = {
        names: obj.names.join(', '),
        date: new Date(obj.notAfter).toLocaleDateString(),
      };
      const said = obj.reused
        ? t('pages.inbounds.certIssue.reused', vars)
        : t('pages.inbounds.certIssue.done', vars);
      messageApi.success(
        doneNote ? t('pages.settings.certIssue.withNote', { said, note: doneNote }) : said,
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
        icon={<SafetyCertificateOutlined aria-hidden />}
        onClick={() => {
          setError('');
          // A name already typed survives closing the dialog; the suggestion only
          // fills an empty one.
          setNames((cur) =>
            cur.length > 0 ? cur : initialNames.map((s) => s.trim()).filter(Boolean),
          );
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
          title={explain}
          description={explainHint}
        />
        <Form layout="vertical">
          <Form.Item
            label={t('pages.inbounds.certIssue.names')}
            tooltip={namesHint}
            htmlFor={`${uid}-names`}
          >
            <Select
              id={`${uid}-names`}
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

// The first candidate a certificate can be issued for: a bare host or a link's
// host, never an IP address, which this issuer does not ask Let's Encrypt for.
export function certNameFrom(...candidates: (string | undefined)[]): string[] {
  for (const raw of candidates) {
    let host = (raw ?? '').trim();
    if (host.includes('://')) {
      try {
        host = new URL(host).hostname;
      } catch {
        continue;
      }
    }
    host = host.replace(/:\d+$/, '');
    if (!host || isAddress(host)) continue;
    return [host];
  }
  return [];
}

// An IP address rather than a name, as a browser's hostname or a typed host.
export function isAddress(host: string): boolean {
  return host.includes(':') || /^[\d.]+$/.test(host);
}
