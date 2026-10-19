import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Col,
  ConfigProvider,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Layout,
  Modal,
  Result,
  Row,
  Space,
  Spin,
  Statistic,
  Switch,
  Table,
  Tag,
  Tooltip,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  GlobalOutlined,
  PlusOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import { useRelaysQuery } from '@/api/queries/useRelaysQuery';
import { useRelayMutations } from '@/api/queries/useRelayMutations';
import type { Reachability, Relay, RelayFormValues } from '@/schemas/relay';

// A relay is a plain server in another country. The panel reaches it over SSH
// once, installs WireGuard and one forwarding rule, and from then on a cisco
// inbound can offer that country in the dropdown its customers already see.
// Nothing of this panel is installed there.

function statusTag(r: Relay, t: (k: string) => string) {
  if (!r.enabled) return <Tag>{t('pages.relays.status.disabled')}</Tag>;
  switch (r.status) {
    case 'ready':
      return r.healthy ? (
        <Tag color="success">{t('pages.relays.status.ready')}</Tag>
      ) : (
        <Tag color="warning">{t('pages.relays.status.silent')}</Tag>
      );
    case 'working':
    case 'pending':
      return <Tag color="processing">{t('pages.relays.status.working')}</Tag>;
    case 'error':
      return <Tag color="error">{t('pages.relays.status.error')}</Tag>;
    default:
      return <Tag>{r.status}</Tag>;
  }
}

function bytes(n: number): string {
  if (!n) return '0';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export default function RelaysPage() {
  const { t } = useTranslation();
  const { antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { relays, isLoading, isFetched, error, refetch } = useRelaysQuery();
  const { createMut, updateMut, removeMut, setEnableMut, probeMut } = useRelayMutations();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Relay | null>(null);
  const [reach, setReach] = useState<{ relay: Relay; result: Reachability } | null>(null);
  const [form] = Form.useForm<RelayFormValues>();

  const summary = useMemo(
    () => ({
      total: relays.length,
      ready: relays.filter((r) => r.enabled && r.status === 'ready' && r.healthy).length,
      trouble: relays.filter((r) => r.enabled && (r.status === 'error' || !r.healthy)).length,
    }),
    [relays],
  );

  const onAdd = useCallback(() => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ sshPort: 22, sshUser: 'root' } as RelayFormValues);
    setFormOpen(true);
  }, [form]);

  const onEdit = useCallback(
    (r: Relay) => {
      setEditing(r);
      form.resetFields();
      form.setFieldsValue({
        name: r.name,
        label: r.label,
        address: r.address,
        sshPort: r.sshPort,
        sshUser: r.sshUser,
      } as RelayFormValues);
      setFormOpen(true);
    },
    [form],
  );

  const onSubmit = useCallback(async () => {
    const values = await form.validateFields();
    const msg = editing
      ? await updateMut.mutateAsync({ id: editing.id, payload: values })
      : await createMut.mutateAsync(values);
    if (msg?.success) {
      setFormOpen(false);
      messageApi.success(t('pages.relays.saved'));
    }
  }, [createMut, editing, form, messageApi, t, updateMut]);

  const onProbe = useCallback(
    async (r: Relay) => {
      try {
        const result = await probeMut.mutateAsync(r.id);
        if (result) setReach({ relay: r, result });
      } catch (e) {
        messageApi.error(e instanceof Error ? e.message : String(e));
      }
    },
    [messageApi, probeMut],
  );

  const onDelete = useCallback(
    (r: Relay) => {
      modal.confirm({
        title: t('pages.relays.confirmDelete', { name: r.label || r.name }),
        content: t('pages.relays.confirmDeleteHint'),
        okButtonProps: { danger: true },
        onOk: async () => {
          const msg = await removeMut.mutateAsync(r.id);
          if (msg?.success) messageApi.success(t('pages.relays.toasts.deleted'));
        },
      });
    },
    [messageApi, modal, removeMut, t],
  );

  const columns = [
    {
      title: t('pages.relays.columns.country'),
      key: 'country',
      render: (_: unknown, r: Relay) => (
        <Space orientation="vertical" size={0}>
          <strong>{r.label || r.name}</strong>
          {r.label ? <span style={{ opacity: 0.6, fontSize: 12 }}>{r.name}</span> : null}
        </Space>
      ),
    },
    {
      title: t('pages.relays.columns.server'),
      key: 'server',
      render: (_: unknown, r: Relay) => (
        <Space orientation="vertical" size={0}>
          <span>{r.address}</span>
          <span style={{ opacity: 0.6, fontSize: 12 }}>
            {r.sshUser}@{r.sshPort} · {r.interface}
          </span>
        </Space>
      ),
    },
    {
      title: t('pages.relays.columns.status'),
      key: 'status',
      render: (_: unknown, r: Relay) => (
        <Space orientation="vertical" size={2}>
          {statusTag(r, t)}
          {r.status === 'error' && r.lastError ? (
            <Tooltip title={r.lastError}>
              <span style={{ fontSize: 12, opacity: 0.7, cursor: 'help' }}>
                {r.lastError.slice(0, 48)}
                {r.lastError.length > 48 ? '…' : ''}
              </span>
            </Tooltip>
          ) : null}
        </Space>
      ),
    },
    {
      title: t('pages.relays.columns.traffic'),
      key: 'traffic',
      render: (_: unknown, r: Relay) =>
        r.tunnelUp ? `↓ ${bytes(r.received)} · ↑ ${bytes(r.sent)}` : '—',
    },
    {
      title: t('pages.relays.columns.usedBy'),
      key: 'usedBy',
      // parseMsg hands the page the raw response whenever validation fails,
      // for any reason, so the component cannot assume the shape it asked
      // for. This field arriving as null is exactly how the page first died.
      render: (_: unknown, r: Relay) =>
        r.inUseBy?.length ? r.inUseBy.join(', ') : <span style={{ opacity: 0.5 }}>—</span>,
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, r: Relay) => (
        <Space>
          <Tooltip title={t('pages.relays.probeHint')}>
            <Button
              size="small"
              icon={<ThunderboltOutlined />}
              loading={probeMut.isPending}
              onClick={() => onProbe(r)}
            />
          </Tooltip>
          <Switch
            size="small"
            checked={r.enabled}
            loading={setEnableMut.isPending}
            onChange={(enable) => setEnableMut.mutate({ id: r.id, enable })}
          />
          <Button size="small" icon={<EditOutlined />} onClick={() => onEdit(r)} />
          <Button size="small" danger icon={<DeleteOutlined />} onClick={() => onDelete(r)} />
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin spinning={!isFetched} delay={200} size="large">
              {error ? (
                <Result
                  status="error"
                  title={t('somethingWentWrong')}
                  subTitle={error instanceof Error ? error.message : String(error)}
                  extra={
                    <Button type="primary" loading={isLoading} onClick={() => refetch()}>
                      {t('refresh')}
                    </Button>
                  }
                />
              ) : (
                <Row gutter={[isMobile ? 8 : 16, isMobile ? 8 : 12]}>
                  <Col span={24}>
                    <Card size="small" className="summary-card">
                      <Row gutter={[16, 12]}>
                        <Col xs={8}>
                          <Statistic
                            title={t('pages.relays.summary.total')}
                            value={String(summary.total)}
                            prefix={<GlobalOutlined />}
                          />
                        </Col>
                        <Col xs={8}>
                          <Statistic
                            title={t('pages.relays.summary.ready')}
                            value={String(summary.ready)}
                            prefix={
                              <CheckCircleOutlined style={{ color: 'var(--ant-color-success)' }} />
                            }
                          />
                        </Col>
                        <Col xs={8}>
                          <Statistic
                            title={t('pages.relays.summary.trouble')}
                            value={String(summary.trouble)}
                          />
                        </Col>
                      </Row>
                    </Card>
                  </Col>
                  <Col span={24}>
                    <Card
                      size="small"
                      title={t('pages.relays.title')}
                      extra={
                        <Button type="primary" icon={<PlusOutlined />} onClick={onAdd}>
                          {t('pages.relays.add')}
                        </Button>
                      }
                    >
                      <Alert
                        type="info"
                        showIcon
                        style={{ marginBottom: 12 }}
                        title={t('pages.relays.intro')}
                        description={t('pages.relays.introHint')}
                      />
                      <Table
                        rowKey="id"
                        size="small"
                        dataSource={relays}
                        columns={columns}
                        pagination={false}
                        locale={{ emptyText: t('pages.relays.empty') }}
                      />
                    </Card>
                  </Col>
                </Row>
              )}
            </Spin>
          </Layout.Content>
        </Layout>
      </Layout>

      <Modal
        open={formOpen}
        title={editing ? t('pages.relays.editTitle') : t('pages.relays.addTitle')}
        onCancel={() => setFormOpen(false)}
        onOk={onSubmit}
        confirmLoading={createMut.isPending || updateMut.isPending}
        destroyOnHidden
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          title={t('pages.relays.credentialsOnce')}
          description={t('pages.relays.credentialsOnceHint')}
        />
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label={t('pages.relays.fields.name')}
            tooltip={t('pages.relays.fields.nameHint')}
            rules={[{ required: true }]}
          >
            <Input placeholder="netherlands" />
          </Form.Item>
          <Form.Item
            name="label"
            label={t('pages.relays.fields.label')}
            tooltip={t('pages.relays.fields.labelHint')}
          >
            <Input placeholder="Netherlands" />
          </Form.Item>
          <Form.Item
            name="address"
            label={t('pages.relays.fields.address')}
            rules={[{ required: true }]}
          >
            <Input placeholder="188.166.94.157" />
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="sshPort" label={t('pages.relays.fields.sshPort')}>
                <InputNumber min={1} max={65535} style={{ width: '100%' }} placeholder="22" />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="sshUser" label={t('pages.relays.fields.sshUser')}>
                <Input placeholder="root" />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item
            name="password"
            label={t('pages.relays.fields.password')}
            tooltip={t('pages.relays.fields.passwordHint')}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="privateKey"
            label={t('pages.relays.fields.privateKey')}
            tooltip={t('pages.relays.fields.privateKeyHint')}
          >
            <Input.TextArea rows={3} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={reach !== null}
        title={t('pages.relays.reachTitle', {
          name: reach?.relay.label || reach?.relay.name || '',
        })}
        onCancel={() => setReach(null)}
        footer={<Button onClick={() => setReach(null)}>{t('close')}</Button>}
      >
        {reach && (
          <>
            <Descriptions size="small" column={1} bordered>
              <Descriptions.Item label={t('pages.relays.reach.address')}>
                {reach.result.Address}
              </Descriptions.Item>
              <Descriptions.Item label={t('pages.relays.reach.where')}>
                {[reach.result.City, reach.result.Country].filter(Boolean).join(', ')}
              </Descriptions.Item>
              <Descriptions.Item label={t('pages.relays.reach.org')}>
                {reach.result.Org}
              </Descriptions.Item>
            </Descriptions>
            <div style={{ marginTop: 12 }}>
              {Object.entries(reach.result.Services ?? {}).map(([name, code]) => (
                <Tag
                  key={name}
                  color={code === '200' || code === 'ok' ? 'success' : undefined}
                  style={{ marginBottom: 6 }}
                >
                  {name}: {code}
                </Tag>
              ))}
            </div>
            {/* A 403 from a bot check looks exactly like a blocked country, so
                only the differences worth acting on are spelled out. */}
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 12 }}
              title={t('pages.relays.reach.reading')}
              description={t('pages.relays.reach.readingHint')}
            />
          </>
        )}
      </Modal>
    </ConfigProvider>
  );
}
