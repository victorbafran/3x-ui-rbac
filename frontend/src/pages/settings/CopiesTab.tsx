import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import {
  CopyOutlined,
  CrownOutlined,
  DeleteOutlined,
  PauseCircleOutlined,
  ReloadOutlined,
  SyncOutlined,
} from '@ant-design/icons';
import { FormProvider, useForm } from 'react-hook-form';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import {
  useCloneMutations,
  useClonesQuery,
  useSetStandbyMutation,
  useStandbyQuery,
} from '@/api/queries/useClones';
import { CloneFormSchema, cloneIsWorking } from '@/schemas/clone';
import type { CloneFormValues, PanelClone } from '@/schemas/clone';
import { IntlUtil } from '@/utils';
import StandbyBanner from './StandbyBanner';

const EMPTY_FORM: CloneFormValues = { address: '', sshPort: 22, sshUser: 'root', password: '' };

function statusTag(c: PanelClone, t: TFunction) {
  switch (c.status) {
    case 'standby':
      return <Tag color="blue">{t('pages.clones.status.standby')}</Tag>;
    case 'main':
      return <Tag color="success">{t('pages.clones.status.main')}</Tag>;
    case 'error':
      return <Tag color="error">{t('pages.clones.status.error')}</Tag>;
    case 'installing':
      return <Tag color="processing">{t('pages.clones.status.installing')}</Tag>;
    default:
      return <Tag color="processing">{t('pages.clones.status.pending')}</Tag>;
  }
}

function modeText(c: PanelClone, t: TFunction): string {
  if (c.mode === 'release') return t('pages.clones.mode.release');
  if (c.mode === 'copied') return t('pages.clones.mode.copied');
  return '—';
}

function when(unixSeconds: number, t: TFunction): string {
  return unixSeconds ? IntlUtil.formatDate(unixSeconds * 1000) : t('pages.clones.never');
}

function clipped(text: string, max = 48) {
  if (!text) return <span style={{ opacity: 0.5 }}>—</span>;
  return (
    <Tooltip title={<bdi>{text}</bdi>}>
      <Typography.Text type="danger" style={{ fontSize: 12, cursor: 'help' }}>
        <bdi>{text.length > max ? `${text.slice(0, max)}…` : text}</bdi>
      </Typography.Text>
    </Tooltip>
  );
}

export default function CopiesTab() {
  const { t } = useTranslation();
  const [modal, modalContextHolder] = Modal.useModal();

  const clonesQuery = useClonesQuery();
  const { clones } = clonesQuery;
  const anyWorking = clones.some(cloneIsWorking);
  // A promotion ends with this panel going on standby: its state is read as often
  // as the copies while one is working, and again the moment one turns main.
  const standbyQuery = useStandbyQuery(anyWorking ? 3000 : 20000);
  const standby = standbyQuery.data?.standby ?? false;
  const { refetch: refetchStandby } = standbyQuery;
  const mainIds = clones
    .filter((c) => c.status === 'main')
    .map((c) => c.id)
    .join(',');
  useEffect(() => {
    if (mainIds) void refetchStandby();
  }, [mainIds, refetchStandby]);
  const setStandbyMut = useSetStandbyMutation();
  const { addMut, syncMut, checkMut, promoteMut, removeMut } = useCloneMutations();

  const methods = useForm<CloneFormValues>({ defaultValues: EMPTY_FORM });
  const onAdd = methods.handleSubmit(async (values) => {
    const msg = await addMut.mutateAsync({
      ...values,
      address: values.address.trim(),
      sshUser: values.sshUser.trim(),
    });
    if (msg?.success) methods.reset(EMPTY_FORM);
  });

  const onMakeMain = () => {
    modal.confirm({
      title: t('pages.standby.makeMainConfirm'),
      content: t('pages.standby.makeMainConfirmDesc'),
      okText: t('pages.standby.makeMain'),
      cancelText: t('cancel'),
      onOk: () => setStandbyMut.mutateAsync(false),
    });
  };

  const onGoStandby = () => {
    modal.confirm({
      title: t('pages.standby.goStandbyConfirm'),
      content: t('pages.standby.goStandbyConfirmDesc'),
      okText: t('pages.standby.goStandby'),
      okButtonProps: { danger: true },
      cancelText: t('cancel'),
      onOk: () => setStandbyMut.mutateAsync(true),
    });
  };

  const onPromote = (c: PanelClone) => {
    modal.confirm({
      title: t('pages.clones.promoteConfirm', { address: c.address }),
      content: t('pages.clones.promoteConfirmDesc', { address: c.address }),
      okText: t('pages.clones.actions.promote'),
      cancelText: t('cancel'),
      onOk: () => promoteMut.mutateAsync(c.id),
    });
  };

  const onRemove = (c: PanelClone) => {
    modal.confirm({
      title: t('pages.clones.removeConfirm', { address: c.address }),
      content: t('pages.clones.removeConfirmDesc'),
      okText: t('pages.clones.actions.remove'),
      okButtonProps: { danger: true },
      cancelText: t('cancel'),
      onOk: () => removeMut.mutateAsync(c.id),
    });
  };

  const columns = [
    {
      title: t('pages.clones.cols.address'),
      key: 'address',
      render: (_: unknown, c: PanelClone) => (
        <Space orientation="vertical" size={0}>
          <bdi>{c.address}</bdi>
          <span style={{ fontSize: 12, opacity: 0.6 }}>
            <bdi>
              {c.sshUser}@{c.sshPort}
            </bdi>
          </span>
        </Space>
      ),
    },
    {
      title: t('pages.clones.cols.status'),
      key: 'status',
      render: (_: unknown, c: PanelClone) => statusTag(c, t),
    },
    {
      title: t('pages.clones.cols.step'),
      key: 'step',
      render: (_: unknown, c: PanelClone) =>
        c.step ? (
          <Space size={6}>
            {cloneIsWorking(c) ? <Spin size="small" /> : null}
            <bdi>{c.step}</bdi>
          </Space>
        ) : (
          <span style={{ opacity: 0.5 }}>—</span>
        ),
    },
    {
      title: t('pages.clones.cols.mode'),
      key: 'mode',
      render: (_: unknown, c: PanelClone) => modeText(c, t),
    },
    {
      title: t('pages.clones.cols.version'),
      key: 'version',
      render: (_: unknown, c: PanelClone) =>
        c.version ? <bdi>{c.version}</bdi> : <span style={{ opacity: 0.5 }}>—</span>,
    },
    {
      title: t('pages.clones.cols.lastSync'),
      key: 'lastSync',
      render: (_: unknown, c: PanelClone) => (
        <Tooltip
          title={c.lastSeen ? `${t('pages.clones.cols.lastSeen')}: ${when(c.lastSeen, t)}` : null}
        >
          <span>{when(c.lastSync, t)}</span>
        </Tooltip>
      ),
    },
    {
      title: t('pages.clones.cols.lastError'),
      key: 'lastError',
      render: (_: unknown, c: PanelClone) => clipped(c.lastError),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, c: PanelClone) => {
        const working = cloneIsWorking(c);
        // The server takes one job per copy, and syncs or promotes only one that
        // finished installing and is not main, never from a panel on standby.
        const canPush = !standby && !working && c.lastSync > 0 && c.status !== 'main';
        const pushHint = standby ? t('pages.standby.refused') : undefined;
        return (
          <Space size={4} wrap>
            <Tooltip title={t('pages.clones.actions.checkHint')}>
              <Button
                size="small"
                icon={<ReloadOutlined aria-hidden />}
                disabled={working}
                loading={checkMut.isPending && checkMut.variables === c.id}
                onClick={() => checkMut.mutate(c.id)}
              >
                {t('pages.clones.actions.check')}
              </Button>
            </Tooltip>
            <Tooltip title={pushHint ?? t('pages.clones.actions.syncHint')}>
              <Button
                size="small"
                icon={<SyncOutlined aria-hidden />}
                disabled={!canPush}
                loading={syncMut.isPending && syncMut.variables === c.id}
                onClick={() => syncMut.mutate(c.id)}
              >
                {t('pages.clones.actions.sync')}
              </Button>
            </Tooltip>
            <Tooltip title={pushHint}>
              <Button
                size="small"
                icon={<CrownOutlined aria-hidden />}
                disabled={!canPush}
                loading={promoteMut.isPending && promoteMut.variables === c.id}
                onClick={() => onPromote(c)}
              >
                {t('pages.clones.actions.promote')}
              </Button>
            </Tooltip>
            <Tooltip title={t('pages.clones.actions.removeHint')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined aria-hidden />}
                aria-label={t('pages.clones.actions.remove')}
                disabled={working}
                onClick={() => onRemove(c)}
              />
            </Tooltip>
          </Space>
        );
      },
    },
  ];

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      {modalContextHolder}
      <StandbyBanner />
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t('pages.clones.intro')}
      </Typography.Paragraph>

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={10}>
          <Card
            size="small"
            title={t('pages.standby.thisPanel')}
            extra={
              standbyQuery.data ? (
                standby ? (
                  <Tag color="warning">{t('pages.standby.tagStandby')}</Tag>
                ) : (
                  <Tag color="success">{t('pages.standby.tagMain')}</Tag>
                )
              ) : null
            }
          >
            {standbyQuery.isError && !standbyQuery.data ? (
              <Alert
                type="error"
                showIcon
                title={t('somethingWentWrong')}
                description={
                  standbyQuery.error instanceof Error
                    ? standbyQuery.error.message
                    : String(standbyQuery.error)
                }
              />
            ) : !standbyQuery.data ? (
              <Spin />
            ) : (
              <>
                <Typography.Paragraph type="secondary">
                  {standby ? t('pages.standby.standbyDesc') : t('pages.standby.mainDesc')}
                </Typography.Paragraph>
                {standby ? (
                  <Button
                    type="primary"
                    icon={<CrownOutlined aria-hidden />}
                    loading={setStandbyMut.isPending}
                    onClick={onMakeMain}
                  >
                    {t('pages.standby.makeMain')}
                  </Button>
                ) : (
                  <Button
                    danger
                    icon={<PauseCircleOutlined aria-hidden />}
                    loading={setStandbyMut.isPending}
                    onClick={onGoStandby}
                  >
                    {t('pages.standby.goStandby')}
                  </Button>
                )}
              </>
            )}
          </Card>
        </Col>

        <Col xs={24} lg={14}>
          <Card size="small" title={t('pages.clones.add.title')}>
            <Typography.Paragraph type="secondary">
              {t('pages.clones.add.hint')}
            </Typography.Paragraph>
            {standby ? (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 12 }}
                title={t('pages.standby.refused')}
              />
            ) : null}
            <FormProvider {...methods}>
              <Form layout="vertical" onFinish={() => void onAdd()}>
                <FormField
                  name="address"
                  label={t('pages.clones.fields.address')}
                  rules={{ validate: rhfZodValidate(CloneFormSchema.shape.address) }}
                >
                  <Input dir="ltr" placeholder="203.0.113.10" autoComplete="off" />
                </FormField>
                <Row gutter={12}>
                  <Col xs={24} sm={10}>
                    <FormField
                      name="sshPort"
                      label={t('pages.clones.fields.sshPort')}
                      rules={{ validate: rhfZodValidate(CloneFormSchema.shape.sshPort) }}
                    >
                      <InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} />
                    </FormField>
                  </Col>
                  <Col xs={24} sm={14}>
                    <FormField
                      name="sshUser"
                      label={t('pages.clones.fields.sshUser')}
                      rules={{ validate: rhfZodValidate(CloneFormSchema.shape.sshUser) }}
                    >
                      <Input dir="ltr" autoComplete="off" />
                    </FormField>
                  </Col>
                </Row>
                <FormField
                  name="password"
                  label={t('pages.clones.fields.password')}
                  rules={{ validate: rhfZodValidate(CloneFormSchema.shape.password) }}
                >
                  <Input.Password dir="ltr" autoComplete="new-password" />
                </FormField>
                <Button
                  type="primary"
                  htmlType="submit"
                  icon={<CopyOutlined aria-hidden />}
                  disabled={standby}
                  loading={addMut.isPending}
                >
                  {t('pages.clones.add.submit')}
                </Button>
              </Form>
            </FormProvider>
          </Card>
        </Col>
      </Row>

      <Card size="small" title={t('pages.clones.list.title')}>
        {clonesQuery.isError ? (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 12 }}
            title={t('somethingWentWrong')}
            description={
              clonesQuery.error instanceof Error
                ? clonesQuery.error.message
                : String(clonesQuery.error)
            }
          />
        ) : null}
        <Table
          rowKey="id"
          size="small"
          loading={clonesQuery.isLoading}
          dataSource={clones}
          columns={columns}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: t('pages.clones.list.empty') }}
        />
      </Card>
    </Space>
  );
}
