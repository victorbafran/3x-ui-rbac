import { useEffect, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Result,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import {
  DeleteOutlined,
  ExperimentOutlined,
  PlusOutlined,
  SearchOutlined,
  SwapOutlined,
  ToolOutlined,
} from '@ant-design/icons';
import { FormProvider, useForm, useWatch } from 'react-hook-form';
import { useQueryClient } from '@tanstack/react-query';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { keys } from '@/api/queryKeys';
import { useArvanFront, useArvanKeyStatus, useArvanMutations } from '@/api/queries/useArvanFront';
import {
  ARVAN_MAX_RESERVES,
  ArvanFrontFormSchema,
  arvanRecordHost,
  type ArvanFrontFormValues,
  type ArvanFrontView,
  type ArvanKeyStatus,
  type ArvanReserve,
} from '@/schemas/arvan';
import type { Relay } from '@/schemas/relay';
import { IntlUtil, LanguageManager } from '@/utils';

interface ArvanFrontModalProps {
  relay: Relay;
  onClose: () => void;
}

interface IpRow {
  ip: string;
  reserve: ArvanReserve | null;
}

function formValuesOf(view: ArvanFrontView): ArvanFrontFormValues {
  return {
    domain: view.domain,
    recordName: view.recordName,
    reserveTarget: view.reserveTarget,
    autoSwap: view.autoSwap,
    dailySwapAt: view.dailySwapAt,
  };
}

function ago(unixSeconds: number, t: TFunction): string {
  if (!unixSeconds) return t('pages.relays.arvan.never');
  const diff = Math.round(unixSeconds - Date.now() / 1000);
  const abs = Math.abs(diff);
  const fmt = new Intl.RelativeTimeFormat(LanguageManager.getLanguage(), { numeric: 'auto' });
  if (abs < 60) return fmt.format(diff, 'second');
  if (abs < 3600) return fmt.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return fmt.format(Math.round(diff / 3600), 'hour');
  return fmt.format(Math.round(diff / 86400), 'day');
}

function When({ at }: { at: number }) {
  const { t } = useTranslation();
  if (!at) return <span style={{ opacity: 0.6 }}>{t('pages.relays.arvan.never')}</span>;
  return (
    <Tooltip title={IntlUtil.formatDate(at * 1000)}>
      <span style={{ cursor: 'help' }}>{ago(at, t)}</span>
    </Tooltip>
  );
}

function keyTag(status: ArvanKeyStatus | undefined, t: TFunction) {
  if (!status?.configured) return <Tag>{t('pages.relays.arvan.key.missing')}</Tag>;
  if (!status.readable) return <Tag color="error">{t('pages.relays.arvan.key.unreadable')}</Tag>;
  return <Tag color="success">{t('pages.relays.arvan.key.configured')}</Tag>;
}

function reserveTag(r: ArvanReserve, t: TFunction) {
  switch (r.status) {
    case 'healthy':
      return <Tag color="success">{t('pages.relays.arvan.ips.healthy')}</Tag>;
    case 'dead':
      return <Tag color="error">{t('pages.relays.arvan.ips.dead')}</Tag>;
    default:
      return <Tag color="processing">{t('pages.relays.arvan.ips.testing')}</Tag>;
  }
}

function activeTag(view: ArvanFrontView, t: TFunction) {
  if (!view.lastCheck) return <Tag>{t('pages.relays.arvan.ips.unchecked')}</Tag>;
  return view.activeHealthy ? (
    <Tag color="success">{t('pages.relays.arvan.ips.healthy')}</Tag>
  ) : (
    <Tag color="error">{t('pages.relays.arvan.ips.unhealthy')}</Tag>
  );
}

function clipped(text: string, max = 40) {
  if (!text) return null;
  return (
    <Tooltip title={text}>
      <Typography.Text type="danger" style={{ fontSize: 12, cursor: 'help' }}>
        {text.length > max ? `${text.slice(0, max)}…` : text}
      </Typography.Text>
    </Tooltip>
  );
}

export default function ArvanFrontModal({ relay, onClose }: ArvanFrontModalProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [modal, modalContextHolder] = Modal.useModal();
  const keyInputId = useId();
  const candidateId = useId();
  const [keyInput, setKeyInput] = useState('');

  const keyQuery = useArvanKeyStatus(true);
  const viewQuery = useArvanFront(relay.id, true);
  const view = viewQuery.data;
  const {
    saveKeyMut,
    clearKeyMut,
    detectMut,
    saveMut,
    prepareMut,
    refillMut,
    testMut,
    swapMut,
    releaseMut,
  } = useArvanMutations(relay.id);

  const methods = useForm<ArvanFrontFormValues>({
    defaultValues: view
      ? formValuesOf(view)
      : { domain: '', recordName: '', reserveTarget: 0, autoSwap: false, dailySwapAt: '' },
  });
  // The view is polled; a fresh copy only replaces the form while the operator
  // has not started editing it.
  const { isDirty } = methods.formState;
  const serverSig = view ? JSON.stringify(formValuesOf(view)) : '';
  useEffect(() => {
    if (serverSig && !isDirty) methods.reset(JSON.parse(serverSig) as ArvanFrontFormValues);
  }, [serverSig, isDirty, methods]);

  // A swap rewrites the relay's address; the list behind the modal shows it.
  const activeIp = view?.activeIp ?? '';
  useEffect(() => {
    if (activeIp && activeIp !== relay.address) {
      void queryClient.invalidateQueries({ queryKey: keys.relays.list() });
    }
  }, [activeIp, relay.address, queryClient]);

  const keyOk = Boolean(keyQuery.data?.configured && keyQuery.data.readable);
  const busy = view?.busy ?? '';
  const prepared = view?.prepared ?? false;
  const bound = view?.bound ?? false;
  const canWork = Boolean(view) && keyOk && bound && !busy;

  const onSaveKey = async () => {
    const msg = await saveKeyMut.mutateAsync(keyInput.trim());
    if (msg?.success) setKeyInput('');
  };

  const onClearKey = () => {
    modal.confirm({
      title: t('pages.relays.arvan.key.confirmClear'),
      content: t('pages.relays.arvan.key.confirmClearHint'),
      okButtonProps: { danger: true },
      onOk: () => clearKeyMut.mutateAsync(),
    });
  };

  const onSaveSettings = methods.handleSubmit(async (values) => {
    const saved = await saveMut.mutateAsync({
      ...values,
      dailySwapAt: values.dailySwapAt.trim(),
    });
    if (saved) methods.reset(formValuesOf(saved));
  });

  const onPickCandidate = (value: string) => {
    const picked = view?.candidates.find((c) => `${c.name}.${c.domain}` === value);
    if (!picked) return;
    methods.setValue('domain', picked.domain, { shouldDirty: true });
    methods.setValue('recordName', picked.name, { shouldDirty: true });
  };

  const onSwap = (ip: string) => {
    modal.confirm({
      title: ip
        ? t('pages.relays.arvan.ips.confirmSwapTo', { ip })
        : t('pages.relays.arvan.ips.confirmSwapNow'),
      content: t('pages.relays.arvan.ips.confirmSwapHint'),
      onOk: () => swapMut.mutateAsync(ip ? { ip } : {}),
    });
  };

  const onRelease = (ip: string) => {
    modal.confirm({
      title: t('pages.relays.arvan.ips.confirmRelease', { ip }),
      content: t('pages.relays.arvan.ips.confirmReleaseHint'),
      okButtonProps: { danger: true },
      onOk: () => releaseMut.mutateAsync({ ip }),
    });
  };

  const pickedDomain = useWatch({ control: methods.control, name: 'domain' });
  const pickedName = useWatch({ control: methods.control, name: 'recordName' });

  const rows: IpRow[] = view
    ? [
        ...(view.activeIp ? [{ ip: view.activeIp, reserve: null }] : []),
        ...view.reserves.map((r) => ({ ip: r.ip, reserve: r })),
      ]
    : [];

  const columns = [
    {
      title: t('pages.relays.arvan.ips.ip'),
      key: 'ip',
      render: (_: unknown, row: IpRow) => (
        <Space size={4}>
          <bdi>{row.ip}</bdi>
          {row.reserve ? null : <Tag color="blue">{t('pages.relays.arvan.ips.active')}</Tag>}
        </Space>
      ),
    },
    {
      title: t('pages.relays.arvan.ips.status'),
      key: 'status',
      render: (_: unknown, row: IpRow) => {
        if (row.reserve) return reserveTag(row.reserve, t);
        if (!view) return null;
        return (
          <Space orientation="vertical" size={0}>
            {activeTag(view, t)}
            {view.activeFails > 0 ? (
              <span style={{ fontSize: 12, opacity: 0.7 }}>
                {t('pages.relays.arvan.ips.fails', { count: view.activeFails })}
              </span>
            ) : null}
          </Space>
        );
      },
    },
    {
      title: t('pages.relays.arvan.ips.iran'),
      key: 'iran',
      render: (_: unknown, row: IpRow) =>
        row.reserve?.iranTotal ? (
          <bdi>
            {row.reserve.iranOk}/{row.reserve.iranTotal}
          </bdi>
        ) : (
          <span style={{ opacity: 0.5 }}>—</span>
        ),
    },
    {
      title: t('pages.relays.arvan.ips.lastCheck'),
      key: 'lastCheck',
      render: (_: unknown, row: IpRow) => (
        <When at={row.reserve ? row.reserve.lastCheck : (view?.lastCheck ?? 0)} />
      ),
    },
    {
      title: t('pages.relays.arvan.ips.error'),
      key: 'error',
      render: (_: unknown, row: IpRow) => clipped(row.reserve?.lastError ?? ''),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, row: IpRow) =>
        row.reserve ? (
          <Space>
            <Tooltip title={t('pages.relays.arvan.ips.swapTo')}>
              <Button
                size="small"
                icon={<SwapOutlined aria-hidden />}
                aria-label={t('pages.relays.arvan.ips.swapTo')}
                disabled={!canWork || !prepared || row.reserve.status !== 'healthy'}
                onClick={() => onSwap(row.ip)}
              />
            </Tooltip>
            <Tooltip title={t('pages.relays.arvan.ips.release')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined aria-hidden />}
                aria-label={t('pages.relays.arvan.ips.release')}
                disabled={!canWork}
                onClick={() => onRelease(row.ip)}
              />
            </Tooltip>
          </Space>
        ) : null,
    },
  ];

  const recordHost = view ? arvanRecordHost(view.recordName, view.domain) : '';

  return (
    <Modal
      open
      width={780}
      title={t('pages.relays.arvan.title', { name: relay.label || relay.name })}
      onCancel={onClose}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
    >
      {modalContextHolder}
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {t('pages.relays.arvan.intro')}
        </Typography.Paragraph>

        <Card
          size="small"
          title={t('pages.relays.arvan.key.title')}
          extra={keyTag(keyQuery.data, t)}
        >
          <Form layout="vertical">
            <Form.Item
              label={t('pages.relays.arvan.key.input')}
              htmlFor={keyInputId}
              extra={t('pages.relays.arvan.key.hint')}
              style={{ marginBottom: 8 }}
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
          {keyQuery.data?.configured ? (
            <Button danger size="small" loading={clearKeyMut.isPending} onClick={onClearKey}>
              {t('pages.relays.arvan.key.clear')}
            </Button>
          ) : null}
        </Card>

        {viewQuery.error ? (
          <Result
            status="error"
            title={t('somethingWentWrong')}
            subTitle={
              viewQuery.error instanceof Error ? viewQuery.error.message : String(viewQuery.error)
            }
            extra={
              <Button loading={viewQuery.isFetching} onClick={() => viewQuery.refetch()}>
                {t('refresh')}
              </Button>
            }
          />
        ) : null}

        {!view && viewQuery.isLoading ? (
          <div style={{ textAlign: 'center', padding: 24 }}>
            <Spin />
          </div>
        ) : null}

        {view ? (
          <>
            <Card
              size="small"
              title={t('pages.relays.arvan.binding.title')}
              extra={
                <Tooltip title={t('pages.relays.arvan.binding.detectHint', { ip: relay.address })}>
                  <Button
                    size="small"
                    icon={<SearchOutlined aria-hidden />}
                    disabled={!keyOk || Boolean(busy)}
                    loading={detectMut.isPending}
                    onClick={() => detectMut.mutate()}
                  >
                    {t('pages.relays.arvan.binding.detect')}
                  </Button>
                </Tooltip>
              }
            >
              {bound ? (
                <Descriptions size="small" column={1} bordered>
                  <Descriptions.Item label={t('pages.relays.arvan.binding.zone')}>
                    <bdi>{view.zone}</bdi>
                  </Descriptions.Item>
                  <Descriptions.Item label={t('pages.relays.arvan.binding.server')}>
                    <Tooltip title={view.serverId}>
                      <bdi>{view.serverName || view.serverId}</bdi>
                    </Tooltip>
                  </Descriptions.Item>
                  <Descriptions.Item label={t('pages.relays.arvan.binding.record')}>
                    {view.recordId ? (
                      <Space orientation="vertical" size={0}>
                        <bdi>
                          {recordHost} → {view.recordIp || '—'}
                        </bdi>
                        {view.recordIp && view.recordIp !== view.activeIp ? (
                          <Typography.Text type="warning" style={{ fontSize: 12 }}>
                            {t('pages.relays.arvan.binding.recordElsewhere', {
                              ip: view.recordIp,
                            })}
                          </Typography.Text>
                        ) : null}
                      </Space>
                    ) : (
                      <Typography.Text type="warning">
                        {t('pages.relays.arvan.binding.noRecord')}
                      </Typography.Text>
                    )}
                  </Descriptions.Item>
                </Descriptions>
              ) : (
                <Typography.Text type="secondary">
                  {t('pages.relays.arvan.binding.notBound')}
                </Typography.Text>
              )}
              {view.candidates.length > 1 ? (
                <Form layout="vertical" style={{ marginTop: 12 }}>
                  <Form.Item
                    label={t('pages.relays.arvan.binding.pickRecord')}
                    htmlFor={candidateId}
                    extra={t('pages.relays.arvan.binding.pickRecordHint')}
                    style={{ marginBottom: 0 }}
                  >
                    <Select
                      id={candidateId}
                      value={pickedDomain ? `${pickedName}.${pickedDomain}` : undefined}
                      onChange={onPickCandidate}
                      options={view.candidates.map((c) => ({
                        value: `${c.name}.${c.domain}`,
                        label: `${arvanRecordHost(c.name, c.domain)} → ${c.ip}`,
                      }))}
                    />
                  </Form.Item>
                </Form>
              ) : null}
            </Card>

            <Card size="small" title={t('pages.relays.arvan.settings.title')}>
              <FormProvider {...methods}>
                <Form layout="vertical">
                  <FormField
                    name="reserveTarget"
                    label={t('pages.relays.arvan.settings.reserveTarget')}
                    extra={t('pages.relays.arvan.settings.reserveTargetHint')}
                    rules={{ validate: rhfZodValidate(ArvanFrontFormSchema.shape.reserveTarget) }}
                  >
                    <InputNumber
                      min={0}
                      max={view.maxReserves || ARVAN_MAX_RESERVES}
                      precision={0}
                      style={{ width: 120 }}
                    />
                  </FormField>
                  <FormField
                    name="autoSwap"
                    label={t('pages.relays.arvan.settings.autoSwap')}
                    extra={t('pages.relays.arvan.settings.autoSwapHint')}
                    valueProp="checked"
                  >
                    <Switch />
                  </FormField>
                  <FormField
                    name="dailySwapAt"
                    label={t('pages.relays.arvan.settings.dailySwapAt')}
                    extra={t('pages.relays.arvan.settings.dailySwapAtHint')}
                    rules={{ validate: rhfZodValidate(ArvanFrontFormSchema.shape.dailySwapAt) }}
                  >
                    <Input placeholder="04:30" dir="ltr" style={{ width: 120 }} allowClear />
                  </FormField>
                  <Button
                    type="primary"
                    loading={saveMut.isPending}
                    disabled={Boolean(busy)}
                    onClick={onSaveSettings}
                  >
                    {t('pages.relays.arvan.settings.save')}
                  </Button>
                </Form>
              </FormProvider>
            </Card>

            <Card
              size="small"
              title={t('pages.relays.arvan.prepare.title')}
              extra={
                prepared ? (
                  <Tag color="success">{t('pages.relays.arvan.prepare.done')}</Tag>
                ) : (
                  <Tag color="warning">{t('pages.relays.arvan.prepare.notDone')}</Tag>
                )
              }
            >
              <Typography.Paragraph type="secondary">
                {t('pages.relays.arvan.prepare.hint')}
              </Typography.Paragraph>
              <Button
                icon={<ToolOutlined aria-hidden />}
                disabled={Boolean(busy)}
                loading={prepareMut.isPending}
                onClick={() => prepareMut.mutate(undefined)}
              >
                {t('pages.relays.arvan.prepare.button')}
              </Button>
            </Card>

            <Card
              size="small"
              title={t('pages.relays.arvan.ips.title')}
              extra={
                <span style={{ fontSize: 12, opacity: 0.7 }}>
                  {t('pages.relays.arvan.ips.addsToday', { count: view.addsToday })}
                </span>
              }
            >
              <Space wrap style={{ marginBottom: 12 }}>
                <Button
                  icon={<PlusOutlined aria-hidden />}
                  disabled={!canWork || !prepared}
                  loading={refillMut.isPending}
                  onClick={() => refillMut.mutate(undefined)}
                >
                  {t('pages.relays.arvan.ips.refill')}
                </Button>
                <Button
                  icon={<ExperimentOutlined aria-hidden />}
                  disabled={!canWork}
                  loading={testMut.isPending}
                  onClick={() => testMut.mutate(undefined)}
                >
                  {t('pages.relays.arvan.ips.testAll')}
                </Button>
                <Button
                  icon={<SwapOutlined aria-hidden />}
                  disabled={!canWork || !prepared}
                  loading={swapMut.isPending}
                  onClick={() => onSwap('')}
                >
                  {t('pages.relays.arvan.ips.swapNow')}
                </Button>
              </Space>
              {!prepared ? (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginBottom: 12 }}
                  title={t('pages.relays.arvan.ips.needsPrepare')}
                />
              ) : null}
              <Table
                rowKey="ip"
                size="small"
                dataSource={rows}
                columns={columns}
                pagination={false}
                scroll={{ x: 'max-content' }}
                locale={{ emptyText: t('pages.relays.arvan.ips.empty') }}
              />
            </Card>

            <Space orientation="vertical" size={2} style={{ width: '100%' }}>
              {busy ? (
                <Space size={8}>
                  <Spin size="small" />
                  <span>
                    {t('pages.relays.arvan.activity.busy', {
                      action: t(`pages.relays.arvan.busyLabels.${busy}`, { defaultValue: busy }),
                    })}
                  </span>
                </Space>
              ) : null}
              {view.lastAction ? (
                <span style={{ fontSize: 12 }}>
                  {t('pages.relays.arvan.activity.lastAction')}: <bdi>{view.lastAction}</bdi>
                </span>
              ) : null}
              {view.lastError ? (
                <Typography.Text type="danger" style={{ fontSize: 12 }}>
                  {t('pages.relays.arvan.activity.lastError')}: <bdi>{view.lastError}</bdi>
                </Typography.Text>
              ) : null}
              <span style={{ fontSize: 12, opacity: 0.7 }}>
                {t('pages.relays.arvan.activity.lastSwap')}: <When at={view.lastSwap} />
              </span>
            </Space>
          </>
        ) : null}
      </Space>
    </Modal>
  );
}
