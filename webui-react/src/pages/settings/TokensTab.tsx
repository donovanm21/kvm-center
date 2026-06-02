import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiDelete, apiGet, apiPost } from '@/api/client';
import { PERMISSIONS } from '@/api/permissions';
import { CheckList } from '@/components/FormBits';
import { Modal } from '@/components/Modal';
import { useToast } from '@/hooks/useToast';
import { useConfirm } from '@/hooks/useConfirm';
import { qk } from '@/queryKeys';

interface APIToken {
  id: string;
  name: string;
  owner_id: string;
  kind: 'user' | 'system';
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
  scopes?: string[];
}

interface CreateReply {
  token: string;     // full secret, shown once
  record: APIToken;
}

export function TokensTab() {
  const qc = useQueryClient();
  const confirmFn = useConfirm();
  const { toast } = useToast();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [expires, setExpires] = useState('');
  const [scopes, setScopes] = useState<string[]>([]);
  const [secret, setSecret] = useState<string | null>(null);

  const list = useQuery({ queryKey: qk.apiTokens(), queryFn: () => apiGet<APIToken[]>('/api/tokens') });

  const create = useMutation({
    mutationFn: () => apiPost<CreateReply>('/api/tokens', {
      name: name.trim(),
      expires_at: expires ? new Date(expires).toISOString() : undefined,
      scopes: scopes.length > 0 ? scopes : undefined,
    }),
    onSuccess: (r) => {
      setSecret(r.token);
      setName('');
      setExpires('');
      setScopes([]);
      qc.invalidateQueries({ queryKey: qk.apiTokens() });
    },
    onError: (e: Error) => toast('error', 'Create failed: ' + e.message),
  });

  const toggleScope = (id: string) =>
    setScopes((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  const revoke = useMutation({
    mutationFn: (id: string) => apiDelete('/api/tokens/' + encodeURIComponent(id)),
    onSuccess: () => {
      toast('success', 'Token revoked');
      qc.invalidateQueries({ queryKey: qk.apiTokens() });
    },
    onError: (e: Error) => toast('error', 'Revoke failed: ' + e.message),
  });

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
        <p style={{ margin: 0, fontSize: 12, color: 'var(--text-muted)' }}>
          <strong>System tokens</strong> — for service accounts and remote admins.
          Fleet-wide view; inherits the creator's admin permissions.
          Users generate personal tokens from the user menu (top-right).
        </p>
        <button type="button" className="btn-primary-sm" onClick={() => { setSecret(null); setCreateOpen(true); }}>
          + Create system token
        </button>
      </div>

      {list.isLoading && <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Loading…</div>}
      {list.error && <div className="tone-pill danger">Failed: {(list.error as Error).message}</div>}

      {list.data && (
        <table className="data-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Kind</th>
              <th>Owner</th>
              <th>ID</th>
              <th>Scopes</th>
              <th>Created</th>
              <th>Expires</th>
              <th>Last used</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.length === 0 && (
              <tr><td colSpan={10} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No tokens yet.</td></tr>
            )}
            {list.data.map((t) => {
              const hasExpiry = t.expires_at && new Date(t.expires_at).getFullYear() > 1000;
              const expired = hasExpiry && new Date(t.expires_at!) < new Date();
              return (
              <tr key={t.id}>
                <td style={{ fontWeight: 500 }}>{t.name}</td>
                <td>
                  <span className={`tone-pill ${t.kind === 'system' ? 'accent' : ''}`} style={{ fontSize: 10 }}>
                    {t.kind === 'system' ? 'system' : 'user'}
                  </span>
                </td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.owner_id}</td>
                <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11 }}>{t.id}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                  {t.scopes && t.scopes.length > 0
                    ? <span title={t.scopes.join(', ')}>{t.scopes.length} scope{t.scopes.length === 1 ? '' : 's'}</span>
                    : <span style={{ color: 'var(--text-faded)' }}>full (inherit)</span>}
                </td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{fmtDate(t.created_at)}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{hasExpiry ? fmtDate(t.expires_at!) : 'never'}</td>
                <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.last_used_at ? fmtDate(t.last_used_at) : 'never'}</td>
                <td>
                  {t.revoked
                    ? <span className="tone-pill danger">revoked</span>
                    : expired
                      ? <span className="tone-pill warn">expired</span>
                      : <span className="tone-pill ok">active</span>}
                </td>
                <td>
                  {!t.revoked && (
                    <button
                      className="btn-danger-sm"
                      onClick={async () => { if (await confirmFn({ title: 'Revoke API token?', body: <>Revoke <code>{t.name}</code>? Any client still using it will fail with 401 on the next call.</> })) revoke.mutate(t.id); }}
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
              Expires (leave blank for no expiry — service account tokens)
              <input
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
                style={inputStyle}
              />
            </label>
            {!expires && (
              <div className="tone-pill warn" style={{ alignSelf: 'flex-start', fontSize: 10 }}>
                No expiry — token is valid until manually revoked
              </div>
            )}
            <div style={{ display: 'grid', gap: 4 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                  Scopes <span style={{ color: 'var(--text-faded)' }}>(optional — leave empty to inherit owner permissions)</span>
                </span>
                {scopes.length > 0 && (
                  <button type="button" className="btn-secondary-sm" onClick={() => setScopes([])} style={{ fontSize: 10 }}>
                    Clear ({scopes.length})
                  </button>
                )}
              </div>
              <CheckList
                items={PERMISSIONS}
                selected={scopes}
                onToggle={toggleScope}
                empty="No permissions defined"
                maxHeight={140}
              />
              <div style={{ fontSize: 10, color: 'var(--text-faded)' }}>
                Scopes can only narrow — a token can never grant a permission the owner doesn't already hold.
              </div>
            </div>
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
