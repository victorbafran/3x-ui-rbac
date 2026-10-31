import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { CertCoversSchema, type CertCovers } from '@/generated/zod';

async function fetchCertCovers(certFile: string, names: string[]): Promise<CertCovers> {
  const msg = await HttpUtil.post(
    '/panel/api/server/certCovers',
    { certFile, names },
    { headers: { 'Content-Type': 'application/json' }, silent: true },
  );
  if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to read the certificate');
  return parseMsg(msg, CertCoversSchema, 'server/certCovers').obj as CertCovers;
}

// Which of names the certificate file a form names is not valid for. A file the
// panel does not use yet answers with an error, and the form then says nothing.
export function useCertCovers(certFile: string, names: string[], enabled: boolean) {
  return useQuery({
    queryKey: keys.server.certCovers(certFile, names),
    queryFn: () => fetchCertCovers(certFile, names),
    enabled: enabled && certFile !== '' && names.length > 0,
    retry: false,
  });
}
