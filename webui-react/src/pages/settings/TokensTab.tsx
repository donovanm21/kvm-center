import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiDelete, apiGet, apiPost } from '@/api/client';
import { Modal } from '@/components/Modal';
import { useToast } from '@/hooks/useToast';

interface APIToken {
  id: string;
  name: string;
  owner_id: string;
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
}

interface CreateReply {
  token: string;     // full secret, shown once
  record: APIToken;
}

export function TokensTab() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [expires, setExpires] = useState('');
  const [secret, setSecret] = useState<string | null>(null);

  const list = useQuery({ queryKey: ['api-tokens'], queryFn: () => apiGet<APIToken[]>('/api/tokens') });

  const create = useMutation({
    mutationFn: () => apiPost<CreateReply>('/api/tokens', {
      name: name.trim(),
      expires_at: expires ? new Date(expires).toISOString() : undefined,
    }),
    onSuccess: (r) => {
      setSecret(r.token);
      setName('');
      setExpires('');
      qc.invalidateQueries({ queryKey: ['api-tokens'] });
    },
    onError: (e: Error) => toast('error', 'Create failed: ' + e.message),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => apiDelete('/api/tokens/' + encodeURIComponent(id)),
    onSuccess: () => {
      toast('success', 'Token revoked');
      qc.invalidateQueries({ queryKey: ['api-tokens'] });
    },
    onError: (e: Error) => toast('error', 'Revoke failed: ' + e.message),
  });

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <p style={{ margin: 0, fontSize: 12, color: 'var(--text-muted)' }}>
          Bearer credentials for scripted use of the kvm-center API. Tokens inherit your account's permissions.
        </p>
        <button type="button" className="btn-primary-sm" onClick={() => { setSecret(null); setCreateOpen(true); }}>
          + Create token
        </button>
      </div>

      {list.isLoading && <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Loading…</div>}
      {list.error && <div className="tone-pill danger">Failed: {(list.error as Error).message}</div>}

      {list.data && (
        <table className="data-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>ID</th>
              <th>Created</th>
              <th>Expires</th>
              <th>Last used</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.length === 0 && (
              <tr><td colSpan={7} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No tokens yet.</td></tr>
            )}
            {list.data.map((t) => (
              <tr key={t.id}>
                <td style={{ fontWeight: 500 }}>{t.name}</td>
                <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11 }}>{t.id}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{fmtDate(t.created_at)}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.expires_at ? fmtDate(t.expires_at) : '—'}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.last_used_at ? fmtDate(t.last_used_at) : 'never'}</td>
                <td>
                  {t.revoked
                    ? <span className="tone-pill danger">revoked</span>
                    : t.expires_at && new Date(t.expires_at) < new Date()
                      ? <span className="tone-pill warn">expired</span>
                      : <span className="tone-pill ok">active</span>}
                </td>
                <td>
                  {!t.revoked && (
                    <button
                      className="btn-danger-sm"
                      onClick={() => { if (confirm(`Revoke "${t.name}"? This cannot be undone.`)) revoke.mutate(t.id); }}
                    >
                      Revoke
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal
        open={createOpen}
        onClose={() => { setCreateOpen(false); setSecret(null); }}
        title={secret ? 'Token created' : 'Create API token'}
        footer={
          secret
            ? <button className="btn-primary-sm" onClick={() => { setCreateOpen(false); setSecret(null); }}>Done</button>
            : (
              <>
                <button className="btn-secondary-sm" onClick={() => setCreateOpen(false)}>Cancel</button>
                <button
                  className="btn-primary-sm"
                  onClick={() => create.mutate()}
                  disabled={!name.trim() || create.isPending}
                >
                  {create.isPending ? 'Creating…' : 'Create'}
                </button>
              </>
            )
        }
      >
        {!secret ? (
          <div style={{ display: 'grid', gap: 10 }}>
            <label style={fieldLabel}>
              Name
              <input
                type="text"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. ci-deploy"
                style={inputStyle}
              />
            </label>
            <label style={fieldLabel}>
              Expires (optional)
              <input
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
                style={inputStyle}
              />
            </label>
          </div>
        ) : (
          <div style={{ display: 'grid', gap: 10 }}>
            <div className="tone-pill warn" style={{ alignSelf: 'flex-start' }}>
              Copy this now — it won't be shown again.
            </div>
            <code style={{
              userSelect: 'all', wordBreak: 'break-all', padding: 10,
              background: 'var(--bg-input)', border: '1px solid var(--border)',
              borderRadius: 4, fontSize: 12, color: 'var(--text-strong)',
            }}>{secret}</code>
            <button
              type="button"
              className="btn-secondary-sm"
              onClick={() => { navigator.clipboard.writeText(secret); toast('success', 'Token copied'); }}
              style={{ alignSelf: 'flex-start' }}
            >
              Copy to clipboard
            </button>
            <pre style={{
              margin: 0, padding: 10, fontSize: 11, color: 'var(--text-muted)',
              background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4,
              whiteSpace: 'pre-wrap',
            }}>
{`# Use with curl:
curl -H "Authorization: Bearer ${secret}" \\
     ${location.origin}/api/hosts

# Use with kvmctl:
kvmctl login --server ${location.origin} --token ${secret}`}
            </pre>
          </div>
        )}
      </Modal>
    </div>
  );
}

function fmtDate(s: string): string {
  if (!s) return '';
  return new Date(s).toLocaleString();
}

const fieldLabel: React.CSSProperties = { display: 'grid', gap: 4, fontSize: 11, color: 'var(--text-muted)' };
const inputStyle: React.CSSProperties = {
  padding: '6px 10px', fontSize: 13,
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontFamily: 'inherit',
};
