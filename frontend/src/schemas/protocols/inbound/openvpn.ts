import { z } from 'zod';

// An OpenVPN inbound is served by a sidecar process, not by xray-core, so its
// settings describe the VPN server: who may log in, what addresses they are
// handed, and where their traffic leaves from.
//
// It mirrors the cisco schema on purpose. A customer who has both protocols
// has one password, and an operator who has set up one should not have to
// learn the other. Where the two differ, it is because the protocols differ:
// OpenVPN has no notion of a group the client picks before connecting, so an
// inbound is one country rather than a list of them.

export const OpenVPNClientSchema = z.object({
  email: z.string().min(1),
  // What the customer types into their OpenVPN client. Stored in the clear
  // because the panel has to be able to show it to them again; the file the
  // sidecar writes holds only a bcrypt hash of it.
  password: z.string().default(''),
  // How many devices may hold this account at once. The panel calls this the
  // IP limit everywhere else; here it is enforced through the server's own
  // management interface rather than through xray's statistics.
  limitIp: z.number().int().min(0).default(0),
  totalGB: z.number().int().min(0).default(0),
  expiryTime: z.number().int().default(0),
  enable: z.boolean().default(true),
  tgId: z
    .union([z.number(), z.string()])
    .transform((v) => Number(v) || 0)
    .default(0),
  subId: z.string().default(''),
  comment: z.string().default(''),
  reset: z.number().int().min(0).default(0),
});
export type OpenVPNClient = z.infer<typeof OpenVPNClientSchema>;

export const OpenVPNInboundSettingsSchema = z.object({
  clients: z.array(OpenVPNClientSchema).default([]),
  // Left empty, the panel derives a /24 from the inbound id that cannot
  // collide with another OpenVPN inbound, with a cisco one, or with the
  // addresses an exit server's tunnel uses.
  network: z.string().default(''),
  netmask: z.string().default('255.255.255.0'),
  dns: z.array(z.string()).default(['1.1.1.1', '8.8.8.8']),
  maxClients: z.number().int().min(0).default(0),
  maxSameClients: z.number().int().min(0).default(2),
  // UDP is faster and is the default. TCP is for a network that drops UDP
  // outright, at the cost of a tunnel carrying TCP inside TCP.
  useTcp: z.boolean().default(false),
  // Which exit server this inbound's traffic leaves through. Empty leaves
  // through this server. OpenVPN cannot offer a choice of country at connect
  // time, so one inbound is one country and a customer with several holds
  // several profiles.
  exit: z.string().default(''),
  // An operator-supplied keypair. Left empty the panel issues its own and
  // reuses it: a certificate reissued on every restart looks to every client
  // like a different server, and the client refuses it.
  certFile: z.string().default(''),
  keyFile: z.string().default(''),
});
export type OpenVPNInboundSettings = z.infer<typeof OpenVPNInboundSettingsSchema>;
