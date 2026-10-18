import { z } from 'zod';

// A cisco inbound is served by an ocserv sidecar, not by xray-core, so its
// settings describe the VPN server rather than an xray handler: who may log
// in, what addresses they are handed, and which certificate is presented.

export const CiscoClientSchema = z.object({
  email: z.string().min(1),
  // What the customer types into their AnyConnect client. Stored in the clear
  // because the panel has to be able to show it to them again; the password
  // file the sidecar writes holds only a bcrypt hash of it.
  password: z.string().default(''),
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
export type CiscoClient = z.infer<typeof CiscoClientSchema>;

export const CiscoInboundSettingsSchema = z.object({
  clients: z.array(CiscoClientSchema).default([]),
  // Left empty, the panel derives a /24 from the inbound id. Two cisco
  // inbounds on one host must not hand out the same addresses or each would
  // route the other's clients.
  network: z.string().default(''),
  netmask: z.string().default('255.255.255.0'),
  dns: z.array(z.string()).default(['1.1.1.1', '8.8.8.8']),
  // DTLS rides UDP and is what makes this protocol fast. Zero means the same
  // port as TCP; set it only when that UDP port is blocked and another is not.
  udpPort: z.number().int().min(0).max(65535).default(0),
  // With UDP blocked for good, offering DTLS costs every connection the
  // fallback timeout and then lands on TCP anyway.
  disableUdp: z.boolean().default(false),
  maxClients: z.number().int().min(0).default(0),
  // How many devices may hold one account at the same time.
  maxSameClients: z.number().int().min(0).default(2),
  // An operator-supplied keypair. Left empty the panel generates a
  // self-signed RSA pair and reuses it. The key must be RSA: Cisco's own
  // Secure Client cannot complete a handshake against an ECDSA server key.
  certFile: z.string().default(''),
  keyFile: z.string().default(''),
});
export type CiscoInboundSettings = z.infer<typeof CiscoInboundSettingsSchema>;
