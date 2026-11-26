import { useEffect, useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
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
  useArvanKeyStatus,
  useArvanServers,
} from '@/api/queries/useArvanFront';
import { ARVAN_CREATE_STEPS, type ArvanServer } from '@/schemas/arvan';

interface ArvanAddModalProps {
  open: boolean;
  onClose: () => void;
  // Opens the ordinary add form with this server's address, for a server the panel did not
  // make: its password is needed there once.
  onUseServer: (address: string, name: string) => void;
}

const NAME_PATTERN = /^[^:,/\\\s]{1,40}$/;

function price(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

export default function ArvanAddModal({ open, onClose, onUseServer }: ArvanAddModalProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const keyInputId = useId();
  const zoneId = useId();
  const sizeId = useId();
  const imageId = useId();
  const nameId = useId();
  const keyQuery = useArvanKeyStatus(open);
  const keyOk = Boolean(keyQuery.data?.configured && keyQuery.data.readable);
  const [zone, setZone] = useState('');
  const catalogQuery = useArvanCatalog(zone, open && keyOk);
  const serversQuery = useArvanServers(open && keyOk);
  const createQuery = useArvanCreateState(open);
  const { saveKeyMut, createMut, deleteServerMut } = useArvanAccountMutations();

  const [keyInput, setKeyInput] = useState('');
  const [pickedSize, setSize] = useState('');
  const [pickedImage, setImage] = useState('');
  const [name, setName] = useState('');

  const catalog = catalogQuery.data;
  // Until the operator picks, the catalog's own choices stand: its zone, the cheapest size and
  // its default image; a pick the zone does not offer falls back the same way.
  const activeZone = zone || catalog?.zone || '';
  const size = catalog?.sizes.some((s) => s.id === pickedSize)
    ? pickedSize
    : (catalog?.sizes[0]?.id ?? '');
  const image = catalog?.images.some((i) => i.id === pickedImage)
    ? pickedImage
    : (catalog?.defaultImage ?? '');

  const state = createQuery.data;
  const running = Boolean(state?.running);
  // a finished make puts its entry server in the list below the window
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

  const onSaveKey = async () => {
    const msg = await saveKeyMut.mutateAsync(keyInput.trim());
    if (msg?.success) setKeyInput('');
  };

  const onCreate = async () => {
    await createMut.mutateAsync({ zone: activeZone, size, image, name: name.trim() });
  };

  const stepIndex = Math.max(0, ARVAN_CREATE_STEPS.indexOf((state?.step ?? 'key') as never));

  const serverColumns = useMemo(
    () => [
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
              disabled={Boolean(s.relay) || s.ips.length === 0}
              onClick={() => onUseServer(s.ips[0], s.name)}
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
    ],
    [deleteServerMut, onUseServer, t],
  );

  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
      width={880}
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

        {!keyOk ? (
          <Card size="small" title={t('pages.relays.arvan.key.title')}>
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
          </Card>
        ) : (
          <Tabs
            items={[
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
                      disabled={!nameOk || !size || !image || running || atLimit}
                    >
                      <Button
                        type="primary"
                        icon={<PlusOutlined aria-hidden />}
                        loading={createMut.isPending || running}
                        disabled={!nameOk || !size || !image || running || atLimit}
                      >
                        {t('pages.relays.arvanAdd.new.create')}
                      </Button>
                    </Popconfirm>
                    {state?.startedAt ? (
                      <Card
                        size="small"
                        title={t('pages.relays.arvanAdd.progress.title', { name: state.name })}
                      >
                        <Steps
                          size="small"
                          orientation="vertical"
                          current={stepIndex}
                          status={
                            state.error ? 'error' : state.step === 'done' ? 'finish' : 'process'
                          }
                          items={ARVAN_CREATE_STEPS.slice(0, -1).map((s) => ({
                            title: t(`pages.relays.arvanAdd.progress.steps.${s}`),
                          }))}
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
                  </Space>
                ),
              },
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
                  </Space>
                ),
              },
            ]}
          />
        )}
      </Space>
    </Modal>
  );
}
