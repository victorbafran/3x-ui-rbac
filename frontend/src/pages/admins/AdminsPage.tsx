import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  Card,
  ConfigProvider,
  Input,
  Layout,
  Modal,
  Result,
  Space,
  Spin,
  Table,
  Tag,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons';
import dayjs from 'dayjs';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useAdminsQuery, usePermissionCatalog, type AdminView } from '@/api/queries/useAdminsQuery';
import { useAdminMutations } from '@/api/queries/useAdminMutations';
import { useInboundOptions } from '@/api/queries/useInboundOptions';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import { GB, type AdminPayload } from '@/schemas/api/admin';
import AdminFormModal from './AdminFormModal';

const unlimited = (n: number | undefined, render: (n: number) => string) =>
  !n || n <= 0 ? '∞' : render(n);

export default function AdminsPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { admins, loading, fetched, fetchError, refetch } = useAdminsQuery();
  const { catalog } = usePermissionCatalog();
  const { add, update, resetPassword, remove, saving } = useAdminMutations();
  const { data: inboundOptions = [] } = useInboundOptions();

  const [formOpen, setFormOpen] = useState(false);
  const [formMode, setFormMode] = useState<'add' | 'edit'>('add');
  const [formAdmin, setFormAdmin] = useState<AdminView | null>(null);
  const [resetTarget, setResetTarget] = useState<AdminView | null>(null);
  const [resetValue, setResetValue] = useState('');

  const onAdd = useCallback(() => {
    setFormMode('add');
    setFormAdmin(null);
    setFormOpen(true);
  }, []);

  const onEdit = useCallback((admin: AdminView) => {
    setFormMode('edit');
    setFormAdmin({ ...admin });
    setFormOpen(true);
  }, []);

  const onSave = useCallback(
    async (payload: AdminPayload) => {
      const msg = formMode === 'edit' && formAdmin ? await update(formAdmin.id, payload) : await add(payload);
      if (msg?.success) messageApi.success(t('pages.admins.toasts.saved'));
      return msg;
    },
    [formMode, formAdmin, update, add, messageApi, t],
  );

  const onDelete = useCallback(
    (admin: AdminView) => {
      modal.confirm({
        title: t('pages.admins.deleteConfirmTitle'),
        content: t('pages.admins.deleteConfirm'),
        okText: t('remove'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await remove(admin.id);
          if (msg?.success) messageApi.success(t('pages.admins.toasts.deleted'));
        },
      });
    },
    [modal, t, remove, messageApi],
  );

  const onResetPassword = useCallback(async () => {
    if (!resetTarget) return;
    if (resetValue.length < 8) {
      messageApi.error(t('pages.admins.toasts.shortPassword'));
      return;
    }
    const msg = await resetPassword(resetTarget.id, resetValue);
    if (msg?.success) {
      messageApi.success(t('pages.admins.toasts.passwordReset'));
      setResetTarget(null);
      setResetValue('');
    }
  }, [resetTarget, resetValue, resetPassword, messageApi, t]);

  const columns = useMemo<ColumnsType<AdminView>>(
    () => [
      { title: t('pages.admins.fields.username'), dataIndex: 'username', key: 'username' },
      {
        title: t('pages.admins.fields.role'),
        dataIndex: 'role',
        key: 'role',
        render: (role: string) => <Tag>{t('pages.admins.roles.' + role)}</Tag>,
      },
      {
        title: t('pages.admins.fields.enabled'),
        dataIndex: 'enabled',
        key: 'enabled',
        render: (enabled: boolean) =>
          enabled ? (
            <Tag color="success" icon={<CheckCircleOutlined />} />
          ) : (
            <Tag color="error" icon={<StopOutlined />} />
          ),
      },
      {
        title: t('pages.admins.fields.usersLimit'),
        dataIndex: 'usersLimit',
        key: 'usersLimit',
        responsive: ['md'],
        render: (n: number) => unlimited(n, String),
      },
      {
        title: t('pages.admins.fields.trafficQuota'),
        dataIndex: 'trafficQuota',
        key: 'trafficQuota',
        responsive: ['md'],
        render: (n: number) => unlimited(n, (v) => `${Math.round((v / GB) * 100) / 100} GB`),
      },
      {
        title: t('pages.admins.fields.expireAt'),
        dataIndex: 'expireAt',
        key: 'expireAt',
        responsive: ['lg'],
        render: (n: number) => (!n ? t('pages.admins.never') : dayjs(n).format('YYYY-MM-DD HH:mm')),
      },
      {
        title: t('pages.admins.assigned'),
        dataIndex: 'inboundIds',
        key: 'inboundIds',
        responsive: ['md'],
        render: (ids: number[]) => ids?.length ?? 0,
      },
      {
        key: 'actions',
        render: (_: unknown, admin: AdminView) => (
          <Space size="small">
            <Button size="small" icon={<EditOutlined />} onClick={() => onEdit(admin)} />
            <Button
              size="small"
              icon={<KeyOutlined />}
              onClick={() => {
                setResetValue('');
                setResetTarget(admin);
              }}
            />
            <Button size="small" danger icon={<DeleteOutlined />} onClick={() => onDelete(admin)} />
          </Space>
        ),
      },
    ],
    [t, onEdit, onDelete],
  );

  const pageClass = useMemo(() => {
    const classes = ['admins-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            {!fetched ? (
              <div style={{ display: 'flex', justifyContent: 'center', padding: 48 }}>
                <Spin size="large" />
              </div>
            ) : fetchError ? (
              <Result
                status="error"
                title={fetchError}
                extra={<Button onClick={() => refetch()}>{t('update')}</Button>}
              />
            ) : (
              <Card
                size="small"
                title={t('pages.admins.title')}
                extra={
                  <Button type="primary" icon={<PlusOutlined />} onClick={onAdd}>
                    {t('pages.admins.addAdmin')}
                  </Button>
                }
              >
                <Table<AdminView>
                  rowKey="id"
                  size={isMobile ? 'small' : 'middle'}
                  loading={loading}
                  columns={columns}
                  dataSource={admins}
                  pagination={false}
                  scroll={{ x: true }}
                />
              </Card>
            )}
          </Layout.Content>
        </Layout>
      </Layout>

      <AdminFormModal
        open={formOpen}
        mode={formMode}
        admin={formAdmin}
        catalog={catalog}
        inboundOptions={inboundOptions}
        saving={saving}
        save={onSave}
        onOpenChange={setFormOpen}
      />

      <Modal
        open={resetTarget !== null}
        title={`${t('pages.admins.resetPassword')} — ${resetTarget?.username ?? ''}`}
        onCancel={() => setResetTarget(null)}
        onOk={onResetPassword}
        okText={t('save')}
        cancelText={t('cancel')}
        confirmLoading={saving}
        destroyOnHidden
      >
        <Input.Password
          autoComplete="new-password"
          placeholder={t('pages.admins.newPassword')}
          value={resetValue}
          onChange={(e) => setResetValue(e.target.value)}
          onPressEnter={onResetPassword}
        />
      </Modal>
    </ConfigProvider>
  );
}
