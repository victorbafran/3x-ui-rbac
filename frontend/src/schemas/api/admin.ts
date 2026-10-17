import { z } from 'zod';

export const AdminRoleSchema = z.enum(['super_admin', 'manager', 'reseller', 'readonly']);
export type AdminRole = z.infer<typeof AdminRoleSchema>;

// The form edits traffic in GB; the server stores bytes.
export const GB = 1024 * 1024 * 1024;

export const AdminFormSchema = z.object({
  username: z.string().trim().min(1, 'pages.admins.toasts.usernameRequired').max(64),
  // Empty means "unchanged" when editing; the create path checks length itself.
  password: z.string().default(''),
  role: AdminRoleSchema,
  enabled: z.boolean().default(true),
  trafficQuotaGb: z.number().min(0).default(0),
  usersLimit: z.number().int().min(0).default(0),
  // Unix milliseconds; 0 means never.
  expireAt: z.number().int().min(0).default(0),
  permissions: z.record(z.string(), z.boolean()).default({}),
  inboundIds: z.array(z.number().int().positive()).default([]),
});
export type AdminFormValues = z.infer<typeof AdminFormSchema>;

// What the admins API accepts for add and update.
export interface AdminPayload {
  username?: string;
  password?: string;
  role: AdminRole;
  enabled: boolean;
  trafficQuota: number;
  usersLimit: number;
  expireAt: number;
  permissions: Record<string, boolean>;
  inboundIds: number[];
}

export function toPayload(values: AdminFormValues, mode: 'add' | 'edit'): AdminPayload {
  const payload: AdminPayload = {
    role: values.role,
    enabled: values.enabled,
    trafficQuota: Math.round(values.trafficQuotaGb * GB),
    usersLimit: values.usersLimit,
    expireAt: values.expireAt,
    permissions: values.permissions,
    inboundIds: values.inboundIds,
  };
  if (mode === 'add') {
    payload.username = values.username;
    payload.password = values.password;
  }
  return payload;
}
