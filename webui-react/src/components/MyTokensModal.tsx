import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { Modal } from './Modal';
import { apiDelete, apiGet, apiPost } from '@/api/client';
import { useToast } from '@/hooks/useToast';

interface APIToken {
  id: string;
  name: string;
  kind: 'user' | 'system';
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
}

interface CreateReply {
  token: string;
  record: APIToken;
}

// Expiry presets — default 30 days matches the server default.
const PRESETS: { label: string; days: number }[] = [
  { label: '7 days',   days: 7 },
  { label: '30 days',  days: 30 },
  { label: '90 days',  days: 90 },
  { label: '1 year',   days: 365 },
];

export function MyTokensModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { toast } = useToast();
  const qc = useQueryClient();
  const [name, setName] = useState('');
  const [days, setDays] = useState(30);
  const [secret, setSecret] = useState<string | null>(null);

  const list = useQuery({
    queryKey: ['my-tokens'],
    queryFn: () => apiGet<APIToken[]>('/api/tokens/me'),
    enabled: open,
  });

  const create = useMutation({
    mutationFn: () => {
      const expires = new Date(Date.now() + days * 86400_000).toISOString();
      return apiPost<CreateReply>('/api/tokens/me', { name: name.trim(), expires_at: expires });
    },
    onSuccess: (r) => {
      setSecret(r.token);
      setName('');
      qc.invalidateQueries({ queryKey: ['my-tokens'] });
    },
    onError: (e: Error) => toast('error', 'Create failed: ' + e.message),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => apiDelete('/api/tokens/me/' + encodeURIComponent(id)),
    onSuccess: () => {
      toast('success', 'Token revoked');
      qc.invalidateQueries({ queryKey: ['my-tokens'] });
    },
    onError: (e: Error) => toast('error', 'Revoke failed: ' + e.message),
  });

  const close = () => { setSecret(null); setName(''); setDays(30); onClose(); };

  return (
    <Modal
      open={open}
      onClose={close}
      title="My API tokens"
      width={640}
      footer={<button className="btn-secondary-sm" onClick={close}>Close</button>}
    >
      <p style={{ margin: '0 0 12px 0', fontSize: 12, color: 'var(--text-muted)' }}>
        Personal tokens for scripting with kvmctl / curl. Inherits your account's permissions.
        Default expiry 30 days — pick another preset if you need longer.
      </p>

      {/* Create form */}
      <div style={createRow}>
        <input
          type="text"
          value={name}
          placeholder="Token name (e.g. laptop-kvmctl)"
          onChange={(e) => setName(e.target.value)}
          style={inputStyle}
        />
        <select value={days} onChange={(e) => setDays(parseInt(e.target.value, 10))} style={{ ...inputStyle, width: 120 }}>
          {PRESETS.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
        </select>
        <button
          type="button"
          className="btn-primary-sm"
          onClick={() => create.mutate()}
          disabled={!name.trim() || create.isPending}
        >
          {create.isPending ? 'Creating…' : 'Create'}
        </button>
      </div>

      {/* Secret — shown once */}
      {secret && (
        <div style={{ marginBottom: 16, display: 'grid', gap: 8 }}>
          <div className="tone-pill warn" style={{ alignSelf: 'flex-start' }}>
            Copy this now — it won't be shown again.
          </div>
          <code style={secretBoxStyle}>{secret}</code>
          <div style={{ display: 'flex', gap: 6 }}>
            <button
              className="btn-secondary-sm"
              onClick={() => { navigator.clipboard.writeText(secret); toast('success', 'Token copied'); }}
            >
              Copy
            </button>
            <button className="btn-secondary-sm" onClick={() => setSecret(null)}>Dismiss</button>
          </div>
        </div>
      )}

      {/* List */}
      {list.isLoading && <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Loading…</div>}
      {list.data && (
        <table className="data-table" style={{ marginTop: 4 }}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Expires</th>
              <th>Last used</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.length === 0 && (
              <tr><td colSpan={5} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No personal tokens yet.</td></tr>
            )}
            {list.data.map((t) => {
              // time.Time zero serialises as "0001-01-01T00:00:00Z" — treat as no expiry, not expired.
              const hasExpiry = t.expires_at && new Date(t.expires_at).getFullYear() > 1000;
              const expired = hasExpiry && new Date(t.expires_at!) < new Date();
              return (
                <tr key={t.id}>
                  <td style={{ fontWeight: 500 }}>{t.name}</td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{hasExpiry ? fmt(t.expires_at!) : 'never'}</td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.last_used_at ? fmt(t.last_used_at) : 'never'}</td>
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
                        onClick={() => { if (confirm(`Revoke "${t.name}"?`)) revoke.mutate(t.id); }}
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
    </Modal>
  );
}

function fmt(s: string): string {
  return new Date(s).toLocaleString();
}

const createRow: React.CSSProperties = { display: 'flex', gap: 6, marginBottom: 14, alignItems: 'center' };
const inputStyle: React.CSSProperties = {
  flex: 1, padding: '6px 10px', fontSize: 13,
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontFamily: 'inherit',
};
const secretBoxStyle: React.CSSProperties = {
  userSelect: 'all', wordBreak: 'break-all', padding: 10,
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, fontSize: 12, color: 'var(--text-strong)',
};
