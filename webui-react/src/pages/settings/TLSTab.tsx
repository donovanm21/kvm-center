import { useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';
import { useConfirm } from '@/hooks/useConfirm';
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
  const confirmFn = useConfirm();
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
          onClick={async () => {
            if (await confirmFn({ title: 'Regenerate TLS cert?', body: 'Overwrite the current cert and key with a fresh self-signed pair? Existing browsers and kvmctl clients will see a cert change and may need to re-trust.' })) {
              regenerate.mutate();
            }
          }}
        >
          {regenerate.isPending ? 'Regenerating…' : 'Regenerate self-signed'}
        </button>
      </div>

      <PendingEnrolments canManage={canManage} />
      <RetiredHosts canManage={canManage} />
      <HypervisorPKI canManage={canManage} />
    </>
  );
}

interface RetiredHost {
  host_id: string;
  removed_at: string;
  removed_by?: string;
  reason?: string;
}

/**
 * Retired hosts panel — surfaces the deny-list and gives the
 * operator an Unretire button. Server-side, deletion of a host
 * adds its host_id to a deny-list so a stale agent can't silently
 * re-enrol with state we just removed. The list was previously
 * server-side-only and the docs / error strings pointed to a UI
 * that didn't exist (v5.60.11 — the operator who tried to re-enrol
 * a rebuilt hypervisor hit the 403 and had no UI path forward).
 */
function RetiredHosts({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient();
  const { toast } = useToast();
  const confirmFn = useConfirm();

  const list = useQuery({
    queryKey: ['retired-hosts'] as const,
    queryFn: () => apiGet<RetiredHost[]>('/api/hosts/retired'),
    refetchInterval: 30_000,
    enabled: canManage,
  });

  const unretire = useMutation({
    mutationFn: (hostID: string) =>
      apiPost(`/api/hosts/${encodeURIComponent(hostID)}/unretire`),
    onSuccess: (_d, hostID) => {
      toast('success', `${hostID} unretired — re-run install.sh on the host to re-enrol`);
      qc.invalidateQueries({ queryKey: ['retired-hosts'] });
      qc.invalidateQueries({ queryKey: ['pending-enrolments'] });
    },
    onError: (e) => toast('error', `Unretire failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  if (!canManage) return null;

  const rows = list.data || [];

  return (
    <div className="panel" style={{ marginBottom: 16 }}>
      <h2 style={{ margin: 0, marginBottom: 4 }}>
        Retired hosts
        {rows.length > 0 && (
          <span className="tone-pill info" style={{ marginLeft: 8, fontSize: 11 }}>
            {rows.length}
          </span>
        )}
      </h2>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
        Hosts you've retired land here. Their <code>host_id</code> is on a
        deny-list so a straggling agent can't silently re-enrol with stale
        state. To bring a host back (e.g. after a rebuild), click <em>Unretire</em>
        — the next <code>install.sh</code> run on that host will produce a
        pending enrolment for you to approve.
      </p>

      {rows.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text-faded)' }}>
          No retired hosts.
        </div>
      )}

      {rows.length > 0 && (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ fontSize: 11, textAlign: 'left', color: 'var(--text-faded)' }}>
              <th style={{ padding: '4px 6px' }}>Host ID</th>
              <th style={{ padding: '4px 6px' }}>Retired at</th>
              <th style={{ padding: '4px 6px' }}>Reason</th>
              <th style={{ padding: '4px 6px', textAlign: 'right' }}>Action</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((h) => (
              <tr key={h.host_id} style={{ borderTop: '1px solid var(--panel-border)' }}>
                <td style={{ padding: '6px', fontFamily: 'monospace', fontSize: 12 }}>{h.host_id}</td>
                <td style={{ padding: '6px', fontSize: 11, color: 'var(--text-faded)' }}>{h.removed_at}</td>
                <td style={{ padding: '6px', fontSize: 11, color: 'var(--text-faded)' }}>{h.reason || '—'}</td>
                <td style={{ padding: '6px', textAlign: 'right' }}>
                  <button
                    className="btn btn-sm"
                    disabled={unretire.isPending}
                    onClick={async () => {
                      if (
                        await confirmFn({
                          title: `Unretire ${h.host_id}?`,
                          body: `Clears ${h.host_id} from the deny-list so it can submit a fresh enrolment request. The host stays out of inventory until install.sh runs on it. Continue?`,
                        })
                      ) {
                        unretire.mutate(h.host_id);
                      }
                    }}
                  >
                    Unretire
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
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
          placeholder="Host ID (e.g. hostD)"
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
      {list.data && (() => {
        const certs = Array.isArray(list.data?.certs) ? list.data.certs : [];
        return (
        <table className="data-table">
          <thead>
            <tr><th>Host</th><th>Serial</th><th>Issued</th><th>Expires</th><th>Status</th><th></th></tr>
          </thead>
          <tbody>
            {certs.length === 0 && (
              <tr><td colSpan={6} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No hypervisor certs issued yet.</td></tr>
            )}
            {certs.map((c) => {
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
        );
      })()}
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

function FilePicker({ label, inputRef, accept }: { label: string; inputRef: React.RefObject<HTMLInputElement | null>; accept: string }) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 4 }}>{label}</div>
      <input type="file" ref={inputRef} accept={accept} style={{ fontSize: 12 }} />
    </div>
  );
}

// ── Pending Enrolments (v5.55.0) ──────────────────────────────────
// Mirror of the Discord-Gateway pair-approve flow but for
// hypervisor PKI. Each row carries the operator-visible fingerprint
// they need to verify against what install.sh printed loudly on
// the host, plus self-asserted system info (OS, kernel, arch).
// Approve signs the CSR; the agent's next 5s poll picks it up.

interface PendingEnrollment {
  id: string;
  host_id: string;
  csr: string;
  fingerprint: string;
  source_ip: string;
  source_version?: string;
  system_info?: Record<string, string>;
  submitted_at: string;
  expires_at: string;
  status: string;
  approved_by?: string;
  approved_at?: string;
  issued_serial?: string;
  delivered_at?: string;
  rejected_by?: string;
  rejected_at?: string;
  reason?: string;
  auto_approved?: boolean;
}

function PendingEnrolments({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient();
  const { toast } = useToast();
  const confirmFn = useConfirm();

  const list = useQuery({
    queryKey: ['pending-enrolments'] as const,
    queryFn: () => apiGet<PendingEnrollment[]>('/api/ca/enrollment-requests-list'),
    refetchInterval: 5_000,
    enabled: canManage,
  });

  const approve = useMutation({
    mutationFn: (id: string) => apiPost(`/api/ca/enrollment-requests/${encodeURIComponent(id)}/approve`),
    onSuccess: () => {
      toast('success', 'Enrolment approved — agent will pick up the cert on its next poll');
      qc.invalidateQueries({ queryKey: ['pending-enrolments'] });
      qc.invalidateQueries({ queryKey: qk.caHypervisors() });
    },
    onError: (e) => toast('error', `Approve failed: ${e instanceof Error ? e.message : String(e)}`),
  });
  const reject = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      apiPost(`/api/ca/enrollment-requests/${encodeURIComponent(id)}/reject`, { reason }),
    onSuccess: () => {
      toast('success', 'Enrolment rejected');
      qc.invalidateQueries({ queryKey: ['pending-enrolments'] });
    },
    onError: (e) => toast('error', `Reject failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const pending = useMemo(
    () => (list.data || []).filter((p) => p.status === 'pending'),
    [list.data],
  );
  const recent = useMemo(
    () => (list.data || []).filter((p) => p.status !== 'pending').slice(0, 5),
    [list.data],
  );

  if (!canManage) return null;

  return (
    <div className="panel" style={{ marginBottom: 16 }}>
      <h2 style={{ margin: 0, marginBottom: 4 }}>
        Pending enrolments
        {pending.length > 0 && (
          <span className="tone-pill warn" style={{ marginLeft: 8, fontSize: 11 }}>
            {pending.length}
          </span>
        )}
      </h2>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
        Hypervisors running <code>install.sh</code> without an enrolment token
        land here. Verify the fingerprint matches what the agent printed on the
        host, then Approve to sign the CSR. The agent's next poll picks the
        cert up automatically. Pending entries expire after 1 hour.
      </p>

      {pending.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text-faded)', padding: '12px 0' }}>
          No enrolments waiting for approval.
        </div>
      )}

      {pending.map((p) => (
        <div
          key={p.id}
          style={{
            border: '1px solid var(--border)',
            borderRadius: 6,
            padding: 12,
            marginBottom: 10,
            background: 'var(--surface-2)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 8 }}>
            <div style={{ fontWeight: 600, fontSize: 14 }}>{p.host_id}</div>
            <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>
              {p.source_ip} · submitted {new Date(p.submitted_at).toLocaleTimeString()}
            </div>
            <div style={{ flex: 1 }} />
            <button
              type="button"
              className="btn-primary-sm"
              disabled={approve.isPending}
              onClick={() => approve.mutate(p.id)}
            >
              Approve
            </button>
            <button
              type="button"
              className="btn-secondary-sm"
              style={{ color: '#F85149', borderColor: '#F8514940' }}
              disabled={reject.isPending}
              onClick={async () => {
                const ok = await confirmFn({
                  title: `Reject enrolment of ${p.host_id}?`,
                  body: (
                    <>
                      The agent will get an HTTP 410 on its next poll and stop trying.
                      Operator will need to re-run <code>install.sh</code> to retry.
                    </>
                  ),
                });
                if (ok) reject.mutate({ id: p.id, reason: 'operator rejected' });
              }}
            >
              Reject
            </button>
          </div>
          <KV label="Fingerprint" value={p.fingerprint} mono wrap />
          {p.system_info && (
            <KV label="System" value={Object.entries(p.system_info).map(([k, v]) => `${k}=${v}`).join('  ')} mono />
          )}
          {p.source_version && <KV label="Submitter" value={p.source_version} />}
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 4 }}>
            Compare this fingerprint to what the agent printed loudly on
            the host. Match → Approve. No match → Reject (someone else may
            be trying to enrol).
          </div>
        </div>
      ))}

      {recent.length > 0 && (
        <details style={{ marginTop: 8 }}>
          <summary style={{ fontSize: 11, color: 'var(--text-muted)', cursor: 'pointer' }}>
            Recent activity ({recent.length})
          </summary>
          <table className="data-table" style={{ marginTop: 8 }}>
            <thead>
              <tr>
                <th>Host</th>
                <th>Status</th>
                <th>Decided by</th>
                <th>When</th>
                <th>Reason</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((p) => (
                <tr key={p.id}>
                  <td>{p.host_id}</td>
                  <td>
                    <span className={`tone-pill ${p.status === 'delivered' || p.status === 'approved' ? 'ok' : 'warn'}`} style={{ fontSize: 10 }}>
                      {p.status}{p.auto_approved ? ' (auto)' : ''}
                    </span>
                  </td>
                  <td style={{ fontSize: 11 }}>{p.approved_by || p.rejected_by || '—'}</td>
                  <td style={{ fontSize: 11, color: 'var(--text-faded)' }}>
                    {p.delivered_at || p.approved_at || p.rejected_at || '—'}
                  </td>
                  <td style={{ fontSize: 11, color: 'var(--text-faded)' }}>{p.reason || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  );
}
