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
  // Which of the inbound's countries this client may pick. Empty means all of
  // them: once the inbound offers any country nobody can connect without
  // naming one, so an empty list must not read as "entitled to none".
  locations: z.array(z.string()).default([]),
});
export type CiscoClient = z.infer<typeof CiscoClientSchema>;

export const CiscoLocationSchema = z.object({
  // The identifier ocserv matches on. It is also a filename and a field in a
  // colon-separated file, so it carries no separator and no space.
  name: z.string().min(1),
  // What the customer reads in the dropdown. Empty shows the name.
  label: z.string().default(''),
  // The interface this country's traffic leaves through — in practice a
  // tunnel to a server standing in that country. Empty leaves through this
  // server, which is the right answer for the country it is already in.
  exit: z.string().default(''),
  // Left empty the panel derives a pool nothing else uses.
  network: z.string().default(''),
  netmask: z.string().default(''),
  dns: z.array(z.string()).default([]),
});
export type CiscoLocation = z.infer<typeof CiscoLocationSchema>;

export const CiscoInboundSettingsSchema = z
  .object({
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
    // The tunnel MTU handed to clients. Zero is the server's default, 1400.
    // Behind an entry server the tunnel's own encapsulation pushes 1400 past
    // the wire limit: small packets pass and large ones vanish, so pages start
    // to load and never finish. 1300 was measured to fix it.
    mtu: z.number().int().min(0).max(1500).default(0),
    // Path MTU discovery needs ICMP to come back; on a path that swallows it a
    // fixed MTU is slower in theory and far steadier in practice.
    mtuDiscovery: z.boolean().default(false),
    // How long, in seconds, the server keeps a phone's session after it stops
    // hearing from it. Zero is 1800. Behind an entry server every phone shares
    // one address, so a phone that changed network cannot be told apart from a
    // second device and each switch leaves a ghost holding a device slot for
    // this long; 90 was measured to clear them.
    mobileDpd: z.number().int().min(0).max(86400).default(0),
    // The customer's address from a BackPack entry server's PROXY header; the
    // inbound then listens on loopback, reachable through entry servers only.
    proxyProtocol: z.boolean().default(false),
    // An operator-supplied keypair. Left empty the panel generates a
    // self-signed RSA pair and reuses it. Use RSA: some versions of Cisco's own
    // Secure Client offer no ECDSA cipher and give up before the certificate is
    // sent, while one measured on 2026-10-21 held an ECDSA session fine.
    certFile: z.string().default(''),
    keyFile: z.string().default(''),
    // The countries this inbound offers. With any present the customer's client
    // shows a Group dropdown and picks where to come out; each one is handed a
    // pool of its own so the host can route it to that country's exit.
    locations: z.array(CiscoLocationSchema).default([]),
  })
  // Loose, not strict: the panel reads settings the form may not show yet,
  // and a strict schema drops them on every save through the form. That is
  // exactly how a measured MTU of 1300 went back to 1400.
  .loose();
export type CiscoInboundSettings = z.infer<typeof CiscoInboundSettingsSchema>;
