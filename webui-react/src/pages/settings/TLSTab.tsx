import { useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';
import { qk } from '@/queryKeys';

interface TLSStatus {
  enabled: boolean;
  subject?: string;
  issuer?: string;
  self_signed?: boolean;
  not_before?: string;
  not_after?: string;
  days_until_expiry?: number;
  fingerprint_sha256?: string;
  dns_names?: string[];
  ip_addresses?: string[];
  cert_path?: string;
  key_path?: string;
  error?: string;
}

/**
 * Settings → TLS: view the current server certificate, upload a
 * replacement (cert + key), regenerate a self-signed cert, or
 * download the active leaf cert. Cert swaps take effect on the next
 * TLS handshake — the server hot-reloads from disk, no restart
 * required.
 */
export function TLSTab() {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const canManage = hasPerm('tls.manage');

  const status = useQuery({
    queryKey: qk.tlsStatus(),
    queryFn: () => apiGet<TLSStatus>('/api/tls/status'),
    refetchInterval: 60_000,
  });

  const regenerate = useMutation({
    mutationFn: () => apiPost('/api/tls/regenerate'),
    onSuccess: () => {
      toast('success', 'Self-signed cert regenerated — new TLS handshakes will use it immediately');
      qc.invalidateQueries({ queryKey: qk.tlsStatus() });
    },
    onError: (e) => toast('error', `Regenerate failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  // Upload state: two file inputs + a client-side mutation that posts
  // multipart/form-data. Server validates the pair before writing.
  const certRef = useRef<HTMLInputElement>(null);
  const keyRef = useRef<HTMLInputElement>(null);
  const [uploadErr, setUploadErr] = useState('');

  const upload = useMutation({
    mutationFn: async () => {
      setUploadErr('');
      const cert = certRef.current?.files?.[0];
      const key = keyRef.current?.files?.[0];
      if (!cert || !key) throw new Error('Both the certificate and the key are required');
      const form = new FormData();
      form.append('cert', cert);
      form.append('key', key);
      const resp = await fetch('/api/tls/upload', { method: 'POST', credentials: 'same-origin', body: form });
      if (!resp.ok) {
        const body = await resp.text();
        try {
          throw new Error(JSON.parse(body).error || body);
        } catch {
          throw new Error(body);
        }
      }
    },
    onSuccess: () => {
      toast('success', 'New TLS cert installed — it is active on the next handshake');
      if (certRef.current) certRef.current.value = '';
      if (keyRef.current) keyRef.current.value = '';
      qc.invalidateQueries({ queryKey: qk.tlsStatus() });
    },
    onError: (e) => setUploadErr(e instanceof Error ? e.message : String(e)),
  });

  const s = status.data;
  const expiryTone = useMemo<'ok' | 'warn' | 'danger' | 'muted'>(() => {
    if (!s?.days_until_expiry && s?.days_until_expiry !== 0) return 'muted';
    if (s.days_until_expiry < 0) return 'danger';
    if (s.days_until_expiry < 30) return 'warn';
    return 'ok';
  }, [s?.days_until_expiry]);

  if (status.isLoading) return <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading TLS status…</div>;
  if (status.error) return <div className="tone-panel danger" style={{ padding: 12, fontSize: 12 }}>Failed to load: {(status.error as Error).message}</div>;
  if (!s?.enabled) {
    return (
      <div className="tone-panel muted" style={{ padding: 14, fontSize: 12 }}>
        TLS is disabled on this server. Restart with <code>--cert</code> and <code>--key</code>
        (or omit both to auto-generate a self-signed cert).
      </div>
    );
  }

  return (
    <>
      {/* Expiry headline — loud when close to or past expiry. */}
      <div className={`tone-panel ${expiryTone}`} style={{ marginBottom: 16, padding: '12px 16px', display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.08em', opacity: 0.8 }}>Certificate expiry</div>
        <div style={{ fontSize: 16, fontWeight: 600 }}>
          {s.days_until_expiry !== undefined
            ? s.days_until_expiry < 0
              ? `Expired ${-s.days_until_expiry}d ago`
              : `${s.days_until_expiry} day${s.days_until_expiry === 1 ? '' : 's'} remaining`
            : '—'}
        </div>
        <div style={{ flex: 1 }} />
        {s.self_signed && <span className="tone-pill warn">self-signed</span>}
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2 style={{ margin: 0, marginBottom: 10 }}>Current certificate</h2>
        <KV label="Subject" value={s.subject} mono />
        <KV label="Issuer" value={s.issuer} mono />
        <KV label="Valid from" value={s.not_before ? new Date(s.not_before).toLocaleString() : '—'} />
        <KV label="Valid until" value={s.not_after ? new Date(s.not_after).toLocaleString() : '—'} />
        <KV label="DNS SANs" value={s.dns_names?.join(', ') || '—'} mono />
        <KV label="IP SANs" value={s.ip_addresses?.join(', ') || '—'} mono />
        <KV label="SHA-256" value={s.fingerprint_sha256} mono wrap />
        <KV label="Cert path" value={s.cert_path} mono />
        <KV label="Key path" value={s.key_path} mono />
        <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
          <a
            href="/api/tls/cert.pem"
            className="btn-secondary-sm"
            style={{ textDecoration: 'none' }}
            download
          >
            Download cert (PEM)
          </a>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2 style={{ margin: 0, marginBottom: 4 }}>Replace certificate</h2>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
          Upload a PEM-encoded certificate (and chain, if applicable) with its matching private key.
          The server validates the pair before committing the files and hot-reloads on the next handshake.
        </p>
        <FilePicker label="Certificate (.crt / .pem)" inputRef={certRef} accept=".crt,.pem,application/x-pem-file,application/x-x509-ca-cert" />
        <FilePicker label="Private key (.key / .pem)" inputRef={keyRef} accept=".key,.pem" />
        {uploadErr && <div className="tone-panel danger" style={{ marginTop: 10, padding: '8px 12px', fontSize: 12 }}>{uploadErr}</div>}
        <div style={{ marginTop: 12 }}>
          <button
            type="button"
            className="btn-primary-sm"
            disabled={!canManage || upload.isPending}
            title={!canManage ? 'Requires tls.manage' : ''}
            onClick={() => upload.mutate()}
          >
            {upload.isPending ? 'Uploading…' : 'Upload and install'}
          </button>
        </div>
      </div>

      <div className="panel">
        <h2 style={{ margin: 0, marginBottom: 4 }}>Regenerate self-signed</h2>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
          Replace the current cert/key pair with a fresh self-signed ECDSA P-256 certificate valid for 10 years.
          Useful when the hostname changed or the current self-signed cert is rotating out.
        </p>
        <button
          type="button"
          className="btn-danger-sm"
          disabled={!canManage || regenerate.isPending}
          title={!canManage ? 'Requires tls.manage' : ''}
          onClick={() => {
            if (confirm('Overwrite the current cert and key with a fresh self-signed pair?')) {
              regenerate.mutate();
            }
          }}
        >
          {regenerate.isPending ? 'Regenerating…' : 'Regenerate self-signed'}
        </button>
      </div>

      <HypervisorPKI canManage={canManage} />
    </>
  );
}

interface HypervisorCert {
  host_id: string;
  serial_hex: string;
  fingerprint: string;
  subject?: string;
  not_before: string;
  not_after: string;
  issued_at: string;
  revoked_at?: string;
  revoke_reason?: string;
}
interface PKIList {
  certs: HypervisorCert[];
  ca: { subject: string; not_before: string; not_after: string };
}

function HypervisorPKI({ canManage }: { canManage: boolean }) {
  const { toast } = useToast();
  const qc = useQueryClient();
  const [hostID, setHostID] = useState('');
  const [enrollToken, setEnrollToken] = useState<string | null>(null);

  const list = useQuery({
    queryKey: qk.caHypervisors(),
    queryFn: () => apiGet<PKIList>('/api/ca/hypervisors'),
  });

  const enroll = useMutation({
    mutationFn: () => apiPost<{ enroll_token: string; expires_in: number }>('/api/ca/enroll', { host_id: hostID.trim() }),
    onSuccess: (r) => {
      setEnrollToken(r.enroll_token);
      toast('success', `Enrollment token issued — valid for ${Math.round(r.expires_in / 60)} min`);
      qc.invalidateQueries({ queryKey: qk.caHypervisors() });
    },
    onError: (e) => toast('error', `Enroll failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const revoke = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      apiPost('/api/ca/hypervisors/' + encodeURIComponent(id) + '/revoke', { reason }),
    onSuccess: () => {
      toast('success', 'Certificate revoked');
      qc.invalidateQueries({ queryKey: qk.caHypervisors() });
    },
    onError: (e) => toast('error', `Revoke failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  return (
    <div className="panel">
      <h2 style={{ margin: 0, marginBottom: 4 }}>Hypervisor PKI</h2>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
        Issue and revoke hypervisor identity certificates. Used by the upcoming mTLS migration channel and signed migration grants.
        Enrollment tokens are one-time and valid for 10 minutes — redeem them from the agent host with{' '}
        <code>install-agent.sh --enroll-token &lt;token&gt; --server https://kvm-center</code>.
      </p>

      {list.data?.ca && (
        <div style={{ fontSize: 11, color: 'var(--text-faded)', marginBottom: 12 }}>
          Cluster CA: <code>{list.data.ca.subject}</code> · expires {new Date(list.data.ca.not_after).toLocaleDateString()}
        </div>
      )}

      <div style={{ display: 'flex', gap: 6, marginBottom: 12 }}>
        <input
          type="text"
          placeholder="Host ID (e.g. hvnode04)"
          value={hostID}
          onChange={(e) => setHostID(e.target.value)}
          disabled={!canManage}
          style={{
            flex: 1, height: 32, padding: '0 10px', fontSize: 13,
            background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4,
            color: 'var(--text-body)', fontFamily: 'inherit',
          }}
        />
        <button
          type="button"
          className="btn-primary-sm"
          disabled={!canManage || !hostID.trim() || enroll.isPending}
          onClick={() => { setEnrollToken(null); enroll.mutate(); }}
        >
          {enroll.isPending ? 'Issuing…' : 'Issue enrollment token'}
        </button>
      </div>

      {enrollToken && (
        <div style={{ marginBottom: 14, display: 'grid', gap: 8 }}>
          <div className="tone-pill warn" style={{ alignSelf: 'flex-start' }}>
            Copy this now — it won't be shown again. 10-minute TTL.
          </div>
          <code style={{
            userSelect: 'all', wordBreak: 'break-all', padding: 10,
            background: 'var(--bg-input)', border: '1px solid var(--border)',
            borderRadius: 4, fontSize: 12, color: 'var(--text-strong)',
          }}>{enrollToken}</code>
          <button
            type="button"
            className="btn-secondary-sm"
            style={{ alignSelf: 'flex-start' }}
            onClick={() => { navigator.clipboard.writeText(enrollToken); toast('success', 'Token copied'); }}
          >
            Copy
          </button>
        </div>
      )}

      {list.isLoading && <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Loading…</div>}
      {list.data && (
        <table className="data-table">
          <thead>
            <tr><th>Host</th><th>Serial</th><th>Issued</th><th>Expires</th><th>Status</th><th></th></tr>
          </thead>
          <tbody>
            {list.data.certs.length === 0 && (
              <tr><td colSpan={6} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No hypervisor certs issued yet.</td></tr>
            )}
            {list.data.certs.map((c) => {
              const revoked = !!c.revoked_at && new Date(c.revoked_at).getFullYear() > 1000;
              return (
                <tr key={c.host_id}>
                  <td style={{ fontWeight: 500 }}>{c.host_id}</td>
                  <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11 }}>{c.serial_hex.slice(0, 12)}…</td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{new Date(c.issued_at).toLocaleString()}</td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{new Date(c.not_after).toLocaleDateString()}</td>
                  <td>
                    {revoked
                      ? <span className="tone-pill danger">revoked</span>
                      : new Date(c.not_after) < new Date()
                        ? <span className="tone-pill warn">expired</span>
                        : <span className="tone-pill ok">active</span>}
                  </td>
                  <td>
                    {!revoked && canManage && (
                      <button
                        className="btn-danger-sm"
                        onClick={() => {
                          const reason = prompt(`Revoke cert for ${c.host_id}? Enter a reason:`);
                          if (reason !== null) revoke.mutate({ id: c.host_id, reason });
                        }}
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

function KV({ label, value, mono, wrap }: { label: string; value?: string; mono?: boolean; wrap?: boolean }) {
  return (
    <div style={{ display: 'flex', padding: '6px 0', gap: 12 }}>
      <div style={{ width: 140, fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
      <div
        style={{
          fontSize: 12,
          color: 'var(--text-body)',
          fontFamily: mono ? "'SF Mono', monospace" : 'inherit',
          wordBreak: wrap ? 'break-all' : 'normal',
          flex: 1,
        }}
      >
        {value || '—'}
      </div>
    </div>
  );
}

function FilePicker({ label, inputRef, accept }: { label: string; inputRef: React.RefObject<HTMLInputElement>; accept: string }) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 4 }}>{label}</div>
      <input type="file" ref={inputRef} accept={accept} style={{ fontSize: 12 }} />
    </div>
  );
}
