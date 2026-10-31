import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
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
  Radio,
  Result,
  Row,
  Select,
  Space,
  Spin,
  Statistic,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  CloudServerOutlined,
  DeleteOutlined,
  EditOutlined,
  GlobalOutlined,
  PlusOutlined,
  ThunderboltOutlined,
  WarningOutlined,
} from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import { useRelaysQuery } from '@/api/queries/useRelaysQuery';
import { useRelayMutations } from '@/api/queries/useRelayMutations';
import {
  ADDRESS6_NOT_CHECKED,
  DEFAULT_TUNNEL_PORT,
  IP_FAMILIES,
  TUNNEL_TOOLS,
  TUNNEL_TRANSPORTS,
  toolOf,
  transportChoices,
  transportEncrypted,
  udpLegPort,
} from '@/schemas/relay';
import type { IPFamily, Reachability, Relay, RelayFormValues } from '@/schemas/relay';
import ArvanFrontModal from './ArvanFrontModal';
import PsiphonCard from './PsiphonCard';
import { bytes } from './bytes';

// A relay is a plain server the panel reaches over SSH once. An exit sits in
// another country: WireGuard and one forwarding rule go on it, and a cisco
// inbound can then offer that country in the dropdown its customers already
// see. A front works the other way round: customers connect to its address,
// and the panel's own inbounds reach it through a tunnel. Nothing of this
// panel is installed on either.

function statusTag(r: Relay, t: (k: string) => string) {
  if (!r.enabled) return <Tag>{t('pages.relays.status.disabled')}</Tag>;
  switch (r.status) {
    case 'ready':
      if (r.healthy) return <Tag color="success">{t('pages.relays.status.ready')}</Tag>;
      // A front has no WireGuard handshake to be missing; what it can lack is
      // the panel's connection to it.
      return r.role === 'front' ? (
        <Tag color="warning">{t('pages.relays.status.notConnected')}</Tag>
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

// A front's tunnel as the operator needs it to open the firewall: the tool,
// the transport and port, and a second leg on the port above when it has one.
function frontTunnelText(r: Relay): string {
  const tool = toolOf(r.tunnel);
  const udp = udpLegPort(tool, r.transport, r.tunnelPort);
  const main = `${tool} ${r.transport}:${r.tunnelPort}`;
  if (udp) return `${main} + udp:${udp}`;
  if (tool === 'backpack' && r.proxyPorts.length) return `${main} + proxy:${r.tunnelPort + 1}`;
  return main;
}

function familyLabel(family: IPFamily, t: TFunction): string {
  if (family === 'both') return t('pages.relays.fields.ipFamilyBoth');
  return family === 'v6' ? 'IPv6' : 'IPv4';
}

// A front's IPv6 as customers are handed it. The mark waits for setup, which
// is when the panel dials the address; before that nothing has been checked.
function frontAddress6(r: Relay, t: TFunction) {
  if (!r.address6) {
    // A domain saved before setup has read the address shows on its own.
    const name6 = r.publicName6 ? (
      <span>
        IPv6 <bdi>{r.publicName6}</bdi>
      </span>
    ) : null;
    if (!r.ipFamily) return name6;
    const notice = (
      <Typography.Text
        type="warning"
        style={{ fontSize: 12, cursor: r.address6Error ? 'help' : undefined }}
      >
        {t('pages.relays.fields.ipFamilyNoAddress')}
      </Typography.Text>
    );
    return (
      <>
        {name6}
        {r.address6Error ? <Tooltip title={r.address6Error}>{notice}</Tooltip> : notice}
      </>
    );
  }
  const checked = r.address6Error !== ADDRESS6_NOT_CHECKED;
  const reason = checked ? r.address6Error : '';
  // The backend joins a failed dial, then the IPv6 domain's DNS check, with "; ";
  // only the DNS check names the domain, so a reason led by it is a DNS fix.
  const reasonKey =
    r.publicName6 && reason.split('; ')[0].includes(r.publicName6)
      ? 'pages.relays.address6Check'
      : 'pages.relays.address6Failed';
  return (
    <>
      <span>
        IPv6 <bdi>{r.address6}</bdi>
        {r.publicName6 ? (
          <>
            {' · '}
            <bdi>{r.publicName6}</bdi>
          </>
        ) : null}{' '}
        {reason ? (
          <WarningOutlined aria-hidden style={{ color: 'var(--ant-color-warning)' }} />
        ) : checked && r.status === 'ready' ? (
          <Tooltip title={t('pages.relays.address6Reached')}>
            <CheckCircleOutlined
              aria-label={t('pages.relays.address6Reached')}
              style={{ color: 'var(--ant-color-success)' }}
            />
          </Tooltip>
        ) : null}
        {r.ipFamily ? (
          <Tag style={{ marginInlineStart: 6 }}>
            {r.ipFamily === 'both' ? t('pages.relays.familyBoth') : t('pages.relays.familyV6')}
          </Tag>
        ) : null}
      </span>
      {reason ? (
        <Tooltip title={reason}>
          <Typography.Text type="warning" style={{ fontSize: 12, cursor: 'help' }}>
            {t(reasonKey, {
              reason: reason.length > 48 ? `${reason.slice(0, 48)}…` : reason,
            })}
          </Typography.Text>
        </Tooltip>
      ) : null}
    </>
  );
}

export default function RelaysPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
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
  const [arvanRelayId, setArvanRelayId] = useState<number | null>(null);
  const arvanRelay = relays.find((r) => r.id === arvanRelayId) ?? null;
  const [form] = Form.useForm<RelayFormValues>();
  const role = Form.useWatch('role', form);
  const isFront = editing ? editing.role === 'front' : role === 'front';
  const tool = Form.useWatch('tool', form) ?? 'backhaul';
  const transport = Form.useWatch('transport', form) ?? 'tcp';
  const tunnelPort = Form.useWatch('tunnelPort', form) ?? DEFAULT_TUNNEL_PORT;
  const udpPort = udpLegPort(tool, transport, tunnelPort);
  const ipFamily = Form.useWatch('ipFamily', form) ?? '';
  const address6 = Form.useWatch('address6', form) ?? '';

  const summary = useMemo(
    () => ({
      total: relays.length,
      ready: relays.filter((r) => r.enabled && r.status === 'ready' && r.healthy).length,
      trouble: relays.filter((r) => r.enabled && (r.status === 'error' || !r.healthy)).length,
    }),
    [relays],
  );

  const pageClass = useMemo(() => {
    const classes = ['relays-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  const onAdd = useCallback(() => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      sshPort: 22,
      sshUser: 'root',
      role: 'exit',
      tunnelPort: DEFAULT_TUNNEL_PORT,
      transport: 'tcp',
      tool: 'backhaul',
      ipFamily: '',
    } as RelayFormValues);
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
        role: r.role,
        publicName: r.publicName,
        address6: r.address6,
        publicName6: r.publicName6,
        ipFamily: r.ipFamily,
        tunnelPort: r.tunnelPort || DEFAULT_TUNNEL_PORT,
        transport: r.transport || 'tcp',
        tool: toolOf(r.tunnel),
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
          <Tag color={r.role === 'front' ? 'blue' : undefined} style={{ marginTop: 2 }}>
            {r.role === 'front' ? t('pages.relays.roles.front') : t('pages.relays.roles.exit')}
          </Tag>
        </Space>
      ),
    },
    {
      title: t('pages.relays.columns.server'),
      key: 'server',
      render: (_: unknown, r: Relay) => (
        <Space orientation="vertical" size={0}>
          <span>
            {r.address}
            {r.role === 'front' && r.publicName ? ` · ${r.publicName}` : ''}
          </span>
          {r.role === 'front' ? frontAddress6(r, t) : null}
          <span style={{ opacity: 0.6, fontSize: 12 }}>
            {r.sshUser}@{r.sshPort} · {r.role === 'front' ? frontTunnelText(r) : r.interface}
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
          {r.role === 'front' && r.status === 'ready' && !r.tunnelUp && r.tunnelDetail ? (
            <Tooltip title={r.tunnelDetail}>
              <span style={{ fontSize: 12, opacity: 0.7, cursor: 'help' }}>
                {r.tunnelDetail.slice(0, 48)}
                {r.tunnelDetail.length > 48 ? '…' : ''}
              </span>
            </Tooltip>
          ) : null}
        </Space>
      ),
    },
    {
      title: t('pages.relays.columns.traffic'),
      key: 'traffic',
      render: (_: unknown, r: Relay) => {
        // Below a front's customer traffic, the ports it opens: a port missing
        // here is a customer who cannot connect through it.
        if (r.role === 'front') {
          const realIp = r.proxyPorts.join(', ');
          return (
            <Space orientation="vertical" size={0}>
              {r.trafficOut > 0 || r.trafficIn > 0 ? (
                <Tooltip title={t('pages.relays.frontTrafficHint')}>
                  <span style={{ cursor: 'help' }}>
                    {`↓ ${bytes(r.trafficOut)} · ↑ ${bytes(r.trafficIn)}`}
                  </span>
                </Tooltip>
              ) : null}
              {r.rateOut > 0 || r.rateIn > 0 ? (
                <span style={{ opacity: 0.6, fontSize: 12 }}>
                  {`↓ ${bytes(r.rateOut)}/s · ↑ ${bytes(r.rateIn)}/s`}
                </span>
              ) : null}
              {r.forwardedPorts.length ? (
                t('pages.relays.forwards', { ports: r.forwardedPorts.join(', ') })
              ) : (
                <span style={{ opacity: 0.5 }}>{t('pages.relays.noPorts')}</span>
              )}
              {realIp && toolOf(r.tunnel) === 'backpack'
                ? t('pages.relays.realIpPorts', { ports: realIp })
                : null}
              {realIp && toolOf(r.tunnel) !== 'backpack' ? (
                <Typography.Text type="warning">
                  {t('pages.relays.realIpNeedsBackpack', { ports: realIp })}
                </Typography.Text>
              ) : null}
            </Space>
          );
        }
        return r.tunnelUp ? `↓ ${bytes(r.received)} · ↑ ${bytes(r.sent)}` : '—';
      },
    },
    {
      title: t('pages.relays.columns.usedBy'),
      key: 'usedBy',
      // parseMsg hands the page the raw response whenever validation fails,
      // for any reason, so the component cannot assume the shape it asked
      // for. This field arriving as null is exactly how the page first died.
      render: (_: unknown, r: Relay) =>
        r.role !== 'front' && r.inUseBy?.length ? (
          r.inUseBy.join(', ')
        ) : (
          <span style={{ opacity: 0.5 }}>—</span>
        ),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, r: Relay) => (
        <Space>
          <Tooltip title={t('pages.relays.probeHint')}>
            <Button
              size="small"
              icon={<ThunderboltOutlined aria-hidden />}
              aria-label={t('pages.relays.probeHint')}
              loading={probeMut.isPending}
              onClick={() => onProbe(r)}
            />
          </Tooltip>
          {r.role === 'front' ? (
            <Tooltip title={t('pages.relays.arvan.open')}>
              <Button
                size="small"
                icon={<CloudServerOutlined aria-hidden />}
                aria-label={t('pages.relays.arvan.open')}
                onClick={() => setArvanRelayId(r.id)}
              />
            </Tooltip>
          ) : null}
          <Switch
            size="small"
            aria-label={t('enabled')}
            checked={r.enabled}
            loading={setEnableMut.isPending}
            onChange={(enable) => setEnableMut.mutate({ id: r.id, enable })}
          />
          <Button
            size="small"
            icon={<EditOutlined aria-hidden />}
            aria-label={t('edit')}
            onClick={() => onEdit(r)}
          />
          <Button
            size="small"
            danger
            icon={<DeleteOutlined aria-hidden />}
            aria-label={t('delete')}
            onClick={() => onDelete(r)}
          />
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={pageClass}>
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
                            prefix={<GlobalOutlined aria-hidden />}
                          />
                        </Col>
                        <Col xs={8}>
                          <Statistic
                            title={t('pages.relays.summary.ready')}
                            value={String(summary.ready)}
                            prefix={
                              <CheckCircleOutlined
                                aria-hidden
                                style={{ color: 'var(--ant-color-success)' }}
                              />
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
                        <Button type="primary" icon={<PlusOutlined aria-hidden />} onClick={onAdd}>
                          {t('pages.relays.add')}
                        </Button>
                      }
                    >
                      <Alert
                        type="info"
                        showIcon
                        style={{ marginBottom: 12 }}
                        title={t('pages.relays.intro')}
                        description={
                          <>
                            {t('pages.relays.introHint')} {t('pages.relays.frontsHint')}
                          </>
                        }
                      />
                      <Table
                        rowKey="id"
                        size="small"
                        dataSource={relays}
                        columns={columns}
                        pagination={false}
                        scroll={{ x: 'max-content' }}
                        locale={{ emptyText: t('pages.relays.empty') }}
                      />
                    </Card>
                  </Col>
                  <Col span={24}>
                    <PsiphonCard />
                  </Col>
                </Row>
              )}
            </Spin>
          </Layout.Content>
        </Layout>
      </Layout>

      <Modal
        open={formOpen}
        title={
          isFront
            ? editing
              ? t('pages.relays.editFrontTitle')
              : t('pages.relays.addFrontTitle')
            : editing
              ? t('pages.relays.editTitle')
              : t('pages.relays.addTitle')
        }
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
            name="role"
            label={t('pages.relays.fields.role')}
            tooltip={t('pages.relays.fields.roleHint')}
          >
            {/* The backend refuses to turn one into the other, because each is
                installed differently, so an edit shows the role but cannot
                change it. */}
            <Radio.Group disabled={editing !== null}>
              <Radio.Button value="exit">{t('pages.relays.fields.roleExit')}</Radio.Button>
              <Radio.Button value="front">{t('pages.relays.fields.roleFront')}</Radio.Button>
            </Radio.Group>
          </Form.Item>
          {isFront ? (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 12 }}
              title={t('pages.relays.frontIntro')}
              description={t('pages.relays.frontIntroHint')}
            />
          ) : null}
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
          {isFront ? (
            <Form.Item
              name="publicName"
              label={t('pages.relays.fields.publicName')}
              tooltip={t('pages.relays.fields.publicNameHint')}
            >
              <Input placeholder="tun.example.com" />
            </Form.Item>
          ) : null}
          {isFront ? (
            <Form.Item
              name="address6"
              label={t('pages.relays.fields.address6')}
              extra={t('pages.relays.fields.address6Hint')}
            >
              <Input placeholder="2001:db8::1" dir="ltr" />
            </Form.Item>
          ) : null}
          {isFront ? (
            <Form.Item
              name="publicName6"
              label={t('pages.relays.fields.publicName6')}
              extra={t('pages.relays.fields.publicName6Hint')}
            >
              <Input placeholder="v6.example.com" dir="ltr" />
            </Form.Item>
          ) : null}
          {isFront ? (
            <Form.Item
              name="ipFamily"
              label={t('pages.relays.fields.ipFamily')}
              tooltip={t('pages.relays.fields.ipFamilyHint')}
              extra={
                ipFamily && !address6.trim()
                  ? t('pages.relays.fields.ipFamilyNoAddress')
                  : undefined
              }
            >
              <Radio.Group>
                {IP_FAMILIES.map((v) => (
                  <Radio.Button key={v} value={v}>
                    {familyLabel(v, t)}
                  </Radio.Button>
                ))}
              </Radio.Group>
            </Form.Item>
          ) : null}
          {isFront ? (
            <Form.Item
              name="tool"
              label={t('pages.relays.fields.tool')}
              tooltip={t('pages.relays.fields.toolHint')}
            >
              {/* Fixed once enrolled, like the role: a different tool is a
                  different binary and unit on the front. */}
              <Radio.Group
                disabled={editing !== null}
                onChange={(e) => {
                  const next = e.target.value as 'backhaul' | 'backpack';
                  // BackPack starts on stealth, which hid cisco's handshake from
                  // the filter on the Iran front; an unknown transport is refused.
                  if (next === 'backpack' && transport === 'tcp') {
                    form.setFieldValue('transport', 'stealth');
                  } else if (!TUNNEL_TRANSPORTS[next].includes(transport)) {
                    form.setFieldValue('transport', 'tcp');
                  }
                }}
              >
                {TUNNEL_TOOLS.map((v) => (
                  <Radio.Button key={v} value={v}>
                    {v === 'backpack' ? 'BackPack' : 'Backhaul'}
                  </Radio.Button>
                ))}
              </Radio.Group>
            </Form.Item>
          ) : null}
          {isFront ? (
            <Row gutter={12}>
              <Col span={12}>
                <Form.Item
                  name="tunnelPort"
                  label={t('pages.relays.fields.tunnelPort')}
                  tooltip={t('pages.relays.fields.tunnelPortHint')}
                  extra={
                    udpPort ? t('pages.relays.fields.udpLegHint', { port: udpPort }) : undefined
                  }
                >
                  <InputNumber
                    min={1}
                    max={65535}
                    style={{ width: '100%' }}
                    placeholder={String(DEFAULT_TUNNEL_PORT)}
                  />
                </Form.Item>
              </Col>
              <Col span={12}>
                <Form.Item
                  name="transport"
                  label={t('pages.relays.fields.transport')}
                  tooltip={t('pages.relays.fields.transportHint')}
                  extra={
                    !transportEncrypted(tool, transport) ? (
                      <Typography.Text type="warning">
                        {t('pages.relays.fields.transportPlain')}
                      </Typography.Text>
                    ) : udpPort ? (
                      // Backhaul's UDP tunnel beside it rides udp, which encrypts nothing.
                      <Typography.Text type="warning">
                        {t('pages.relays.fields.transportUdpPlain', { port: udpPort })}
                      </Typography.Text>
                    ) : undefined
                  }
                >
                  <Select
                    options={transportChoices(toolOf(tool), editing?.transport).map((v) => ({
                      value: v,
                      label: transportEncrypted(tool, v)
                        ? t('pages.relays.fields.transportEncrypted', { transport: v })
                        : v,
                    }))}
                  />
                </Form.Item>
              </Col>
            </Row>
          ) : null}
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

      {arvanRelay ? (
        <ArvanFrontModal relay={arvanRelay} onClose={() => setArvanRelayId(null)} />
      ) : null}
    </ConfigProvider>
  );
}
