import { z } from 'zod';

// A copy is this panel installed on another server from its full backup and
// left on standby, so it can take over when this one is lost.

export const CLONE_STATUSES = ['pending', 'installing', 'standby', 'main', 'error'] as const;
export type CloneStatus = (typeof CLONE_STATUSES)[number];

// "release" installed the fork's latest release; "copied" put this panel's own
// newer build over it.
export const CLONE_MODES = ['', 'release', 'copied'] as const;
export type CloneMode = (typeof CLONE_MODES)[number];

export const PanelCloneSchema = z.object({
  id: z.number(),
  address: z.string(),
  sshPort: z.number().default(22),
  sshUser: z.string().default('root'),
  status: z.enum(CLONE_STATUSES).catch('pending'),
  // The step running now; "" when nothing is.
  step: z.string().default(''),
  mode: z.enum(CLONE_MODES).catch(''),
  version: z.string().default(''),
  lastError: z.string().default(''),
  lastSeen: z.number().default(0),
  lastSync: z.number().default(0),
  createdAt: z.number().default(0),
});
export type PanelClone = z.infer<typeof PanelCloneSchema>;

// Go sends an empty slice as null, which default() would let through.
export const PanelCloneListSchema = z
  .array(PanelCloneSchema)
  .nullish()
  .transform((v) => v ?? []);

export function cloneIsWorking(c: PanelClone): boolean {
  return c.status === 'pending' || c.status === 'installing' || c.step !== '';
}

export const StandbyStateSchema = z.object({ standby: z.boolean().default(false) });
export type StandbyState = z.infer<typeof StandbyStateSchema>;

// The password is used once to install the panel's own key and never stored.
export const CloneFormSchema = z.object({
  address: z
    .string()
    .trim()
    .pipe(z.union([z.ipv4(), z.ipv6(), z.hostname()], { error: 'pages.clones.errors.address' })),
  sshPort: z
    .number({ error: 'pages.clones.errors.sshPort' })
    .int('pages.clones.errors.sshPort')
    .min(1, 'pages.clones.errors.sshPort')
    .max(65535, 'pages.clones.errors.sshPort'),
  sshUser: z.string().trim().min(1, 'pages.clones.errors.sshUser'),
  password: z.string().min(1, 'pages.clones.errors.password'),
});
export type CloneFormValues = z.infer<typeof CloneFormSchema>;
