import { useEffect, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Select,
  Space,
  Steps,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd';
import {
  CloudServerOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import { useQueryClient } from '@tanstack/react-query';

import { keys } from '@/api/queryKeys';
import {
  useArvanAccountMutations,
  useArvanCatalog,
  useArvanCreateState,
  useArvanDomains,
  useArvanKeyStatus,
  useArvanServers,
} from '@/api/queries/useArvanFront';
import type { ArvanPurpose, ArvanServer } from '@/schemas/arvan';
import { TUNNEL_TOOLS, TUNNEL_TRANSPORTS, type TunnelTool } from '@/schemas/relay';

interface ArvanAddModalProps {
  open: boolean;
  onClose: () => void;
}

const NAME_PATTERN = /^[^:,/\\\s]{1,40}$/;
const RECORD_PATTERN =
  /^(@|[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*)$/;
const DEFAULT_PURPOSE: ArvanPurpose = {
  role: 'front',
  tool: 'backpack',
  transport: 'stealth',
  domain: '',
  record: '',
};

function price(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

// A name Arvan and this panel both take, from a server's own name.
function relayName(name: string): string {
  return name.replace(/[:,/\\\s]+/g, '-').slice(0, 40);
}

function purposeOk(p: ArvanPurpose, exitAllowed: boolean): boolean {
  if (p.role === 'exit') return exitAllowed;
  return !p.domain || RECORD_PATTERN.test(p.record);
}

function recordHost(record: string, domain: string): string {
  return record === '@' ? domain : `${record}.${domain}`;
}

// The questions every server from Arvan answers before it is added: what it is for, which
// tunnel carries it, and which domain record its IP goes behind.
function PurposeFields({
  value,
  onChange,
  exitAllowed,
  domains,
}: {
  value: ArvanPurpose;
  onChange: (next: ArvanPurpose) => void;
  exitAllowed: boolean;
  domains: string[];
}) {
  const { t } = useTranslation();
  const toolId = useId();
  const transportId = useId();
  const domainId = useId();
  const recordId = useId();
  const set = (patch: Partial<ArvanPurpose>) => onChange({ ...value, ...patch });
  const tool = (
    TUNNEL_TOOLS.includes(value.tool as TunnelTool) ? value.tool : 'backpack'
  ) as TunnelTool;
  const transports = TUNNEL_TRANSPORTS[tool];
  return (
    <>
      <Form.Item
        label={t('pages.relays.arvanAdd.purpose.role')}
        extra={exitAllowed ? undefined : t('pages.relays.arvanAdd.purpose.exitIran')}
      >
        <Radio.Group
          value={value.role}
          onChange={(e) => set({ role: e.target.value as ArvanPurpose['role'] })}
          options={[
            { value: 'front', label: t('pages.relays.arvanAdd.purpose.front') },
            {
              value: 'exit',
              label: t('pages.relays.arvanAdd.purpose.exit'),
              disabled: !exitAllowed,
            },
          ]}
        />
      </Form.Item>
      {value.role === 'front' ? (
        <>
          <Space wrap size={12}>
            <Form.Item label={t('pages.relays.arvanAdd.purpose.tool')} htmlFor={toolId}>
              <Select
                id={toolId}
                style={{ width: 160 }}
                value={tool}
                onChange={(next: TunnelTool) =>
                  set({ tool: next, transport: TUNNEL_TRANSPORTS[next][0] })
                }
                options={TUNNEL_TOOLS.map((tl) => ({
                  value: tl,
                  label: tl === 'backpack' ? 'BackPack' : 'Backhaul',
                }))}
              />
            </Form.Item>
            <Form.Item label={t('pages.relays.arvanAdd.purpose.transport')} htmlFor={transportId}>
              <Select
                id={transportId}
                style={{ width: 160 }}
                value={transports.includes(value.transport) ? value.transport : transports[0]}
                onChange={(next: string) => set({ transport: next })}
                options={transports.map((tr) => ({ value: tr, label: tr }))}
              />
            </Form.Item>
          </Space>
          {/* the record stays beside its domain at any width; the hint goes under both */}
          <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
            <Form.Item
              label={t('pages.relays.arvanAdd.purpose.domain')}
              htmlFor={domainId}
              style={{ flex: '0 1 220px', minWidth: 0 }}
            >
              <Select
                id={domainId}
                style={{ width: '100%' }}
                value={value.domain}
                onChange={(next: string) => set({ domain: next })}
                options={[
                  { value: '', label: t('pages.relays.arvanAdd.purpose.noDomain') },
                  ...domains.map((d) => ({ value: d, label: d })),
                ]}
              />
            </Form.Item>
            {value.domain ? (
              <Form.Item
                label={t('pages.relays.arvanAdd.purpose.record')}
                htmlFor={recordId}
                validateStatus={
                  value.record && !RECORD_PATTERN.test(value.record) ? 'error' : undefined
                }
                style={{ flex: '0 1 220px', minWidth: 0 }}
              >
                <Input
                  id={recordId}
                  dir="ltr"
                  style={{ width: '100%' }}
                  value={value.record}
                  onChange={(e) => set({ record: e.target.value.trim().toLowerCase() })}
                />
              </Form.Item>
            ) : null}
          </div>
          {value.domain && RECORD_PATTERN.test(value.record) ? (
            <Typography.Paragraph type="secondary" style={{ marginTop: -16, fontSize: 12 }}>
              {t('pages.relays.arvanAdd.purpose.recordHint', {
                fqdn: recordHost(value.record, value.domain),
              })}
            </Typography.Paragraph>
          ) : null}
        </>
      ) : null}
    </>
  );
}

export default function ArvanAddModal({ open, onClose }: ArvanAddModalProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const keyInputId = useId();
  const zoneId = useId();
  const sizeId = useId();
  const imageId = useId();
  const nameId = useId();
  const adoptNameId = useId();
  const adoptPasswordId = useId();
  const keyQuery = useArvanKeyStatus(open);
  const keyOk = Boolean(keyQuery.data?.configured && keyQuery.data.readable);
  const [zone, setZone] = useState('');
  const catalogQuery = useArvanCatalog(zone, open && keyOk);
  const serversQuery = useArvanServers(open && keyOk);
  const domainsQuery = useArvanDomains(open && keyOk);
  const createQuery = useArvanCreateState(open);
  const { saveKeyMut, createMut, deleteServerMut, adoptMut } = useArvanAccountMutations();

  const [changingKey, setChangingKey] = useState(false);
  const [keyInput, setKeyInput] = useState('');
  const [pickedSize, setSize] = useState('');
  const [pickedImage, setImage] = useState('');
  const [name, setName] = useState('');
  const [purpose, setPurpose] = useState<ArvanPurpose>(DEFAULT_PURPOSE);
  const [adopting, setAdopting] = useState<ArvanServer | null>(null);
  const [adoptName, setAdoptName] = useState('');
  const [adoptPassword, setAdoptPassword] = useState('');
  const [adoptPurpose, setAdoptPurpose] = useState<ArvanPurpose>(DEFAULT_PURPOSE);

  const catalog = catalogQuery.data;
  const domains = domainsQuery.data ?? [];
  // Until the operator picks, the catalog's own choices stand: its zone, the cheapest size and
  // its default image; a pick the zone does not offer falls back the same way.
  const activeZone = zone || catalog?.zone || '';
  const size = catalog?.sizes.some((s) => s.id === pickedSize)
    ? pickedSize
    : (catalog?.sizes[0]?.id ?? '');
  const image = catalog?.images.some((i) => i.id === pickedImage)
    ? pickedImage
    : (catalog?.defaultImage ?? '');
  // an exit only where customers would come out outside Iran
  const outsideIran = (code: string) => {
    const z = catalog?.zones.find((zz) => zz.code === code);
    return Boolean(z && z.country && z.country.toLowerCase() !== 'iran');
  };

  const state = createQuery.data;
  const running = Boolean(state?.running);
  // a finished run puts its server in the relays list behind the window
  useEffect(() => {
    if (state && !state.running && state.finishedAt) {
      void queryClient.invalidateQueries({ queryKey: keys.relays.root() });
      void queryClient.invalidateQueries({ queryKey: keys.relays.arvan.servers() });
    }
  }, [queryClient, state]);

  const chosen = catalog?.sizes.find((s) => s.id === size);
  const nameOk = NAME_PATTERN.test(name.trim());
  const atLimit = Boolean(
    catalog && catalog.serverLimit > 0 && catalog.serverCount >= catalog.serverLimit,
  );
  const createReady =
    nameOk &&
    Boolean(size && image) &&
    !running &&
    !atLimit &&
    purposeOk(purpose, outsideIran(activeZone));
  const adoptReady =
    adopting !== null &&
    NAME_PATTERN.test(adoptName.trim()) &&
    adoptPassword.length > 0 &&
    !running &&
    purposeOk(adoptPurpose, outsideIran(adopting.zone));

  const onSaveKey = async () => {
    const msg = await saveKeyMut.mutateAsync(keyInput.trim());
    if (msg?.success) {
      setKeyInput('');
      setChangingKey(false);
    }
  };

  const onCreate = async () => {
    await createMut.mutateAsync({ ...purpose, zone: activeZone, size, image, name: name.trim() });
  };

  const onAdopt = async () => {
    if (!adopting) return;
    const msg = await adoptMut.mutateAsync({
      ...adoptPurpose,
      zone: adopting.zone,
      serverId: adopting.id,
      name: adoptName.trim(),
      password: adoptPassword,
    });
    if (msg?.success) {
      setAdopting(null);
      setAdoptPassword('');
    }
  };

  const startAdopt = (s: ArvanServer) => {
    setAdopting(s);
    setAdoptName(relayName(s.name));
    setAdoptPassword('');
    setAdoptPurpose(DEFAULT_PURPOSE);
  };

  const steps = state?.steps ?? [];
  const stepIndex =
    state?.step === 'done' ? steps.length : Math.max(0, steps.indexOf(state?.step ?? ''));

  const serverColumns = [
    { title: t('pages.relays.arvanAdd.servers.name'), dataIndex: 'name', key: 'name' },
    { title: t('pages.relays.arvanAdd.servers.zone'), dataIndex: 'zone', key: 'zone' },
    {
      title: t('pages.relays.arvanAdd.servers.ips'),
      key: 'ips',
      render: (_: unknown, s: ArvanServer) => (
        <Space size={4} wrap>
          {s.ips.map((ip) => (
            <bdi key={ip}>{ip}</bdi>
          ))}
        </Space>
      ),
    },
    {
      title: t('pages.relays.arvanAdd.servers.status'),
      key: 'status',
      render: (_: unknown, s: ArvanServer) => (
        <Tag color={s.status === 'active' ? 'success' : 'default'}>{s.status}</Tag>
      ),
    },
    {
      title: t('pages.relays.arvanAdd.servers.relay'),
      key: 'relay',
      render: (_: unknown, s: ArvanServer) =>
        s.relay ? (
          <Tag color="blue">{s.relay}</Tag>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, s: ArvanServer) => (
        <Space size={4}>
          <Button
            size="small"
            disabled={Boolean(s.relay) || s.ips.length === 0 || running}
            onClick={() => startAdopt(s)}
          >
            {t('pages.relays.arvanAdd.servers.use')}
          </Button>
          <Popconfirm
            title={t('pages.relays.arvanAdd.servers.confirmDelete', { name: s.name })}
            description={t('pages.relays.arvanAdd.servers.confirmDeleteHint')}
            okButtonProps={{ danger: true }}
            okText={t('pages.relays.arvanAdd.servers.delete')}
            disabled={Boolean(s.relay)}
            onConfirm={() => deleteServerMut.mutateAsync({ zone: s.zone, id: s.id })}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined aria-hidden />}
              aria-label={t('pages.relays.arvanAdd.servers.delete')}
              disabled={Boolean(s.relay)}
              title={
                s.relay ? t('pages.relays.arvanAdd.servers.inUse', { relay: s.relay }) : undefined
              }
            />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const keyForm = (
    <Form layout="vertical">
      <Form.Item
        label={t('pages.relays.arvan.key.input')}
        htmlFor={keyInputId}
        extra={t('pages.relays.arvanAdd.keyHint')}
        style={{ marginBottom: 0 }}
      >
        <Space.Compact style={{ width: '100%' }}>
          <Input.Password
            id={keyInputId}
            dir="ltr"
            autoComplete="new-password"
            placeholder="apikey ••••••••-••••-••••-••••-••••••••••••"
            value={keyInput}
            onChange={(e) => setKeyInput(e.target.value)}
          />
          <Button
            type="primary"
            disabled={!keyInput.trim()}
            loading={saveKeyMut.isPending}
            onClick={onSaveKey}
          >
            {t('pages.relays.arvan.key.save')}
          </Button>
        </Space.Compact>
      </Form.Item>
    </Form>
  );

  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
      width={900}
      title={
        <Space>
          <CloudServerOutlined aria-hidden />
          {t('pages.relays.arvanAdd.title')}
        </Space>
      }
      destroyOnHidden
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {t('pages.relays.arvanAdd.intro')}
        </Typography.Paragraph>

        <Card
          size="small"
          title={t('pages.relays.arvan.key.title')}
          extra={
            keyOk ? (
              <Space size={8}>
                <Tag color="success">{t('pages.relays.arvan.key.configured')}</Tag>
                <Button size="small" onClick={() => setChangingKey((v) => !v)}>
                  {t('pages.relays.arvanAdd.changeKey')}
                </Button>
              </Space>
            ) : (
              <Tag>{t('pages.relays.arvan.key.missing')}</Tag>
            )
          }
        >
          {!keyOk || changingKey ? (
            keyForm
          ) : (
            <Typography.Text type="secondary">
              {t('pages.relays.arvanAdd.keyReady')}
            </Typography.Text>
          )}
        </Card>

        {state?.startedAt ? (
          <Card
            size="small"
            title={t('pages.relays.arvanAdd.progress.title', { name: state.name })}
          >
            <Steps
              size="small"
              orientation="vertical"
              current={stepIndex}
              status={state.error ? 'error' : state.step === 'done' ? 'finish' : 'process'}
              items={steps.map((s) => ({ title: t(`pages.relays.arvanAdd.progress.steps.${s}`) }))}
            />
            {state.ip ? (
              <Typography.Paragraph style={{ marginBottom: 4 }}>
                {t('pages.relays.arvanAdd.progress.ip')} <bdi>{state.ip}</bdi>
              </Typography.Paragraph>
            ) : null}
            {state.error ? <Alert type="error" showIcon title={state.error} /> : null}
            {!state.running && !state.error && state.step === 'done' ? (
              <Alert
                type="success"
                showIcon
                title={t('pages.relays.arvanAdd.progress.done', { name: state.name })}
              />
            ) : null}
          </Card>
        ) : null}

        {keyOk ? (
          <Tabs
            items={[
              {
                key: 'servers',
                label: t('pages.relays.arvanAdd.tabs.servers'),
                children: (
                  <Space orientation="vertical" size={8} style={{ width: '100%' }}>
                    <Space wrap>
                      <Button
                        size="small"
                        icon={<ReloadOutlined aria-hidden />}
                        loading={serversQuery.isFetching}
                        onClick={() => void serversQuery.refetch()}
                      >
                        {t('pages.relays.arvanAdd.servers.refresh')}
                      </Button>
                      <Typography.Text type="secondary">
                        {t('pages.relays.arvanAdd.servers.hint')}
                      </Typography.Text>
                    </Space>
                    {serversQuery.error ? (
                      <Alert
                        type="error"
                        showIcon
                        title={String((serversQuery.error as Error).message)}
                      />
                    ) : null}
                    <Table
                      size="small"
                      rowKey={(s) => `${s.zone}/${s.id}`}
                      columns={serverColumns}
                      dataSource={serversQuery.data ?? []}
                      loading={serversQuery.isLoading}
                      pagination={false}
                      scroll={{ x: true }}
                    />
                    {adopting ? (
                      <Card
                        size="small"
                        title={t('pages.relays.arvanAdd.adopt.title', { name: adopting.name })}
                        extra={
                          <Button size="small" onClick={() => setAdopting(null)}>
                            {t('pages.relays.arvanAdd.adopt.cancel')}
                          </Button>
                        }
                      >
                        <Form layout="vertical" disabled={running}>
                          <Space wrap size={12} align="start">
                            <Form.Item
                              label={t('pages.relays.arvanAdd.new.name')}
                              htmlFor={adoptNameId}
                              validateStatus={
                                adoptName && !NAME_PATTERN.test(adoptName.trim())
                                  ? 'error'
                                  : undefined
                              }
                            >
                              <Input
                                id={adoptNameId}
                                dir="ltr"
                                maxLength={40}
                                style={{ width: 220 }}
                                value={adoptName}
                                onChange={(e) => setAdoptName(e.target.value)}
                              />
                            </Form.Item>
                            <Form.Item
                              label={t('pages.relays.arvanAdd.adopt.password')}
                              htmlFor={adoptPasswordId}
                              extra={t('pages.relays.arvanAdd.adopt.passwordHint')}
                            >
                              <Input.Password
                                id={adoptPasswordId}
                                autoComplete="new-password"
                                style={{ width: 220 }}
                                value={adoptPassword}
                                onChange={(e) => setAdoptPassword(e.target.value)}
                              />
                            </Form.Item>
                          </Space>
                          <PurposeFields
                            value={adoptPurpose}
                            onChange={setAdoptPurpose}
                            exitAllowed={outsideIran(adopting.zone)}
                            domains={domains}
                          />
                        </Form>
                        <Button
                          type="primary"
                          loading={adoptMut.isPending}
                          disabled={!adoptReady}
                          onClick={onAdopt}
                        >
                          {t('pages.relays.arvanAdd.adopt.submit')}
                        </Button>
                      </Card>
                    ) : null}
                  </Space>
                ),
              },
              {
                key: 'new',
                label: t('pages.relays.arvanAdd.tabs.new'),
                children: (
                  <Space orientation="vertical" size={12} style={{ width: '100%' }}>
                    {catalogQuery.error ? (
                      <Alert
                        type="error"
                        showIcon
                        title={String((catalogQuery.error as Error).message)}
                      />
                    ) : null}
                    <Form layout="vertical" disabled={running}>
                      <Form.Item label={t('pages.relays.arvanAdd.new.zone')} htmlFor={zoneId}>
                        <Select
                          id={zoneId}
                          value={activeZone || undefined}
                          loading={catalogQuery.isFetching}
                          onChange={setZone}
                          options={(catalog?.zones ?? []).map((z) => ({
                            value: z.code,
                            label: `${z.city} · ${z.dc} (${z.code})`,
                          }))}
                        />
                      </Form.Item>
                      <Form.Item
                        label={t('pages.relays.arvanAdd.new.size')}
                        htmlFor={sizeId}
                        extra={t('pages.relays.arvanAdd.new.sizeHint')}
                      >
                        <Select
                          id={sizeId}
                          value={size || undefined}
                          onChange={setSize}
                          options={(catalog?.sizes ?? []).map((s) => ({
                            value: s.id,
                            label: t('pages.relays.arvanAdd.new.sizeOption', {
                              cpus: s.cpus,
                              ram: s.memoryGb,
                              disk: s.diskGb,
                              hour: price(s.pricePerHour),
                              month: price(s.pricePerMonth),
                            }),
                          }))}
                        />
                      </Form.Item>
                      <Form.Item label={t('pages.relays.arvanAdd.new.image')} htmlFor={imageId}>
                        <Select
                          id={imageId}
                          value={image || undefined}
                          onChange={setImage}
                          options={(catalog?.images ?? []).map((i) => ({
                            value: i.id,
                            label: `${i.distribution} ${i.version}`,
                          }))}
                        />
                      </Form.Item>
                      <Form.Item
                        label={t('pages.relays.arvanAdd.new.name')}
                        htmlFor={nameId}
                        extra={t('pages.relays.arvanAdd.new.nameHint')}
                        validateStatus={name && !nameOk ? 'error' : undefined}
                      >
                        <Input
                          id={nameId}
                          value={name}
                          dir="ltr"
                          maxLength={40}
                          onChange={(e) => setName(e.target.value)}
                        />
                      </Form.Item>
                      <PurposeFields
                        value={purpose}
                        onChange={setPurpose}
                        exitAllowed={outsideIran(activeZone)}
                        domains={domains}
                      />
                    </Form>
                    {atLimit ? (
                      <Alert
                        type="warning"
                        showIcon
                        title={t('pages.relays.arvanAdd.new.atLimit', {
                          count: catalog?.serverCount,
                          limit: catalog?.serverLimit,
                        })}
                      />
                    ) : null}
                    <Popconfirm
                      title={t('pages.relays.arvanAdd.new.confirm', { name: name.trim() })}
                      description={t('pages.relays.arvanAdd.new.confirmHint', {
                        hour: price(chosen?.pricePerHour ?? 0),
                        month: price(chosen?.pricePerMonth ?? 0),
                      })}
                      okText={t('pages.relays.arvanAdd.new.create')}
                      onConfirm={onCreate}
                      disabled={!createReady}
                    >
                      <Button
                        type="primary"
                        icon={<PlusOutlined aria-hidden />}
                        loading={createMut.isPending || running}
                        disabled={!createReady}
                      >
                        {t('pages.relays.arvanAdd.new.create')}
                      </Button>
                    </Popconfirm>
                  </Space>
                ),
              },
            ]}
          />
        ) : null}
      </Space>
    </Modal>
  );
}
