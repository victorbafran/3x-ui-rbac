import { useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, DatePicker, Divider, Form, Input, InputNumber, Modal, Select, Switch, Typography } from 'antd';
import dayjs from 'dayjs';
import { Controller, FormProvider, useWatch } from 'react-hook-form';

import { FormField, useZodForm } from '@/components/form/rhf';
import type { InboundOption } from '@/schemas/client';
import {
  AdminFormSchema,
  AdminRoleSchema,
  GB,
  toPayload,
  type AdminFormValues,
  type AdminPayload,
  type AdminRole,
} from '@/schemas/api/admin';
import type { AdminView, PermissionCatalog } from '@/api/queries/useAdminsQuery';

interface AdminFormModalProps {
  open: boolean;
  mode: 'add' | 'edit';
  admin: AdminView | null;
  catalog: PermissionCatalog | undefined;
  inboundOptions: InboundOption[];
  saving: boolean;
  save: (payload: AdminPayload) => Promise<{ success?: boolean; msg?: string } | undefined>;
  onOpenChange: (open: boolean) => void;
}

const GROUPS = ['sections', 'clients', 'inbounds'] as const;

function templateFor(catalog: PermissionCatalog | undefined, role: AdminRole): Record<string, boolean> {
  const template = catalog?.templates?.[role] ?? {};
  const out: Record<string, boolean> = {};
  for (const key of catalog?.keys ?? []) out[key] = Boolean(template[key]);
  return out;
}

function defaultsFor(
  admin: AdminView | null,
  catalog: PermissionCatalog | undefined,
): AdminFormValues {
  const role = (AdminRoleSchema.safeParse(admin?.role).success
    ? admin?.role
    : 'reseller') as AdminRole;
  return {
    username: admin?.username ?? '',
    password: '',
    role,
    enabled: admin?.enabled ?? true,
    trafficQuotaGb: admin ? Math.round(((admin.trafficQuota ?? 0) / GB) * 100) / 100 : 0,
    usersLimit: admin?.usersLimit ?? 0,
    expireAt: admin?.expireAt ?? 0,
    // The stored overrides sit on top of the role template, exactly as the gate reads them.
    permissions: { ...templateFor(catalog, role), ...(admin?.permissions ?? {}) },
    inboundIds: admin?.inboundIds ?? [],
  };
}

export default function AdminFormModal({
  open,
  mode,
  admin,
  catalog,
  inboundOptions,
  saving,
  save,
  onOpenChange,
}: AdminFormModalProps) {
  const { t } = useTranslation();
  const form = useZodForm<AdminFormValues>(AdminFormSchema, {
    defaultValues: defaultsFor(admin, catalog),
  });
  const { control, handleSubmit, reset, setValue, setError } = form;

  useEffect(() => {
    if (open) reset(defaultsFor(admin, catalog));
  }, [open, admin, catalog, reset]);

  const role = useWatch({ control, name: 'role' });
  const permissions = useWatch({ control, name: 'permissions' }) ?? {};
  const isSuper = role === 'super_admin';

  const roles = useMemo<AdminRole[]>(() => {
    const known = Object.keys(catalog?.templates ?? {}).filter(
      (r): r is AdminRole => AdminRoleSchema.safeParse(r).success,
    );
    return known.length ? known : [...AdminRoleSchema.options];
  }, [catalog]);

  const grouped = useMemo(() => {
    const keys = catalog?.keys ?? [];
    return GROUPS.map((group) => ({
      group,
      keys: keys.filter((k) => k.startsWith(`${group}.`)),
    })).filter((g) => g.keys.length > 0);
  }, [catalog]);

  const applyTemplate = (nextRole: AdminRole) => {
    setValue('permissions', templateFor(catalog, nextRole), { shouldDirty: true });
  };

  const onSubmit = handleSubmit(async (values) => {
    if (mode === 'add' && values.password.length < 8) {
      setError('password', { type: 'manual', message: 'pages.admins.toasts.shortPassword' });
      return;
    }
    const msg = await save(toPayload(values, mode));
    if (msg?.success) onOpenChange(false);
  });

  return (
    <Modal
      open={open}
      title={mode === 'add' ? t('pages.admins.addAdmin') : t('pages.admins.editAdmin')}
      onCancel={() => onOpenChange(false)}
      onOk={onSubmit}
      okText={t('save')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      width={720}
      destroyOnHidden
    >
      <FormProvider {...form}>
        <Form layout="vertical" component="div">
          <FormField name="username" label={t('pages.admins.fields.username')} required>
            <Input autoComplete="off" disabled={mode === 'edit'} />
          </FormField>
          {mode === 'add' && (
            <FormField name="password" label={t('pages.admins.fields.password')} required>
              <Input.Password autoComplete="new-password" />
            </FormField>
          )}

          <Form.Item label={t('pages.admins.fields.role')}>
            <Controller
              control={control}
              name="role"
              render={({ field }) => (
                <Select
                  value={field.value}
                  onChange={(value: AdminRole) => {
                    field.onChange(value);
                    applyTemplate(value);
                  }}
                  options={roles.map((r) => ({ value: r, label: t('pages.admins.roles.' + r) }))}
                />
              )}
            />
          </Form.Item>

          <Form.Item label={t('pages.admins.fields.enabled')}>
            <Controller
              control={control}
              name="enabled"
              render={({ field }) => <Switch checked={field.value} onChange={field.onChange} />}
            />
          </Form.Item>

          <FormField name="trafficQuotaGb" label={t('pages.admins.fields.trafficQuota')}>
            <InputNumber min={0} step={1} style={{ width: '100%' }} />
          </FormField>
          <FormField name="usersLimit" label={t('pages.admins.fields.usersLimit')}>
            <InputNumber min={0} step={1} precision={0} style={{ width: '100%' }} />
          </FormField>

          <Form.Item label={t('pages.admins.fields.expireAt')}>
            <Controller
              control={control}
              name="expireAt"
              render={({ field }) => (
                <DatePicker
                  style={{ width: '100%' }}
                  showTime
                  value={field.value ? dayjs(field.value) : null}
                  onChange={(d) => field.onChange(d ? d.valueOf() : 0)}
                  placeholder={t('pages.admins.never')}
                />
              )}
            />
          </Form.Item>

          <Form.Item label={t('pages.admins.fields.inbounds')}>
            <Controller
              control={control}
              name="inboundIds"
              render={({ field }) => (
                <Select
                  mode="multiple"
                  value={field.value}
                  onChange={field.onChange}
                  placeholder={t('pages.admins.noInbounds')}
                  optionFilterProp="label"
                  options={inboundOptions.map((ib) => ({
                    value: ib.id,
                    label: ib.remark ? `${ib.remark} (#${ib.id})` : `#${ib.id}`,
                  }))}
                />
              )}
            />
          </Form.Item>

          <Divider>{t('pages.admins.fields.permissions')}</Divider>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
            <Button size="small" disabled={isSuper} onClick={() => applyTemplate(role)}>
              {t('pages.admins.resetToTemplate')}
            </Button>
          </div>
          {grouped.map(({ group, keys }) => (
            <div key={group} style={{ marginBottom: 16 }}>
              <Typography.Text strong>{t('pages.admins.groups.' + group)}</Typography.Text>
              <div
                style={{
                  display: 'grid',
                  gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                  gap: '8px 16px',
                  marginTop: 8,
                }}
              >
                {keys.map((key) => (
                  <label key={key} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Switch
                      size="small"
                      checked={isSuper || Boolean(permissions[key])}
                      disabled={isSuper}
                      onChange={(checked) =>
                        setValue('permissions', { ...permissions, [key]: checked }, { shouldDirty: true })
                      }
                    />
                    <span>{t('pages.admins.perms.' + key)}</span>
                  </label>
                ))}
              </div>
            </div>
          ))}
        </Form>
      </FormProvider>
    </Modal>
  );
}
