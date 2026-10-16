import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { Modal } from './Modal';
import { CheckList } from './FormBits';
import { apiDelete, apiGet, apiPost } from '@/api/client';
import { PERMISSIONS } from '@/api/permissions';
import { useZodForm } from '@/forms/useZodForm';
import { tokenFormSchema } from '@/forms/schemas';
import { useConfirm } from '@/hooks/useConfirm';
import { useToast } from '@/hooks/useToast';
import { qk } from '@/queryKeys';

interface APIToken {
  id: string;
  name: string;
  kind: 'user' | 'system';
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
  scopes?: string[];
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
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [secret, setSecret] = useState<string | null>(null);
  // Empty = full inheritance from owner perms (legacy behaviour).
  // Non-empty = additional constraint, can only narrow.
  const [scopes, setScopes] = useState<string[]>([]);

  const form = useZodForm(tokenFormSchema, {
    defaultValues: { name: '', days: 30 },
  });

  const list = useQuery({
    queryKey: qk.myTokens(),
    queryFn: () => apiGet<APIToken[]>('/api/tokens/me'),
    enabled: open,
  });

  const create = useMutation({
    mutationFn: (values: { name: string; days: number }) => {
      const expires = new Date(Date.now() + values.days * 86400_000).toISOString();
      return apiPost<CreateReply>('/api/tokens/me', {
        name: values.name,
        expires_at: expires,
        scopes: scopes.length > 0 ? scopes : undefined,
      });
    },
    onSuccess: (r) => {
      setSecret(r.token);
      form.reset({ name: '', days: form.getValues('days') });
      setScopes([]);
      qc.invalidateQueries({ queryKey: qk.myTokens() });
    },
    onError: (e: Error) => toast('error', 'Create failed: ' + e.message),
  });

  const onSubmit = form.handleSubmit((values) => create.mutate(values));

  const revoke = useMutation({
    mutationFn: (id: string) => apiDelete('/api/tokens/me/' + encodeURIComponent(id)),
    onSuccess: () => {
      toast('success', 'Token revoked');
      qc.invalidateQueries({ queryKey: qk.myTokens() });
    },
    onError: (e: Error) => toast('error', 'Revoke failed: ' + e.message),
  });

  const close = () => { setSecret(null); form.reset({ name: '', days: 30 }); setScopes([]); onClose(); };

  const toggleScope = (id: string) =>
    setScopes((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  const nameErr = form.formState.errors.name?.message;

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
      <form onSubmit={onSubmit}>
        <div style={createRow}>
          <input
            type="text"
            placeholder="Token name (e.g. laptop-kvmctl)"
            {...form.register('name')}
            style={{ ...inputStyle, borderColor: nameErr ? '#F85149' : 'var(--border)' }}
          />
          <select {...form.register('days', { valueAsNumber: true })} style={{ ...inputStyle, width: 120 }}>
            {PRESETS.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
          </select>
          <button type="submit" className="btn-primary-sm" disabled={create.isPending}>
            {create.isPending ? 'Creating…' : 'Create'}
          </button>
        </div>
        {nameErr && (
          <div style={{ marginTop: 6, fontSize: 11, color: '#F85149' }} role="alert">{nameErr}</div>
        )}
        <div style={{ marginTop: 10, marginBottom: 14 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 4 }}>
            <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>
              Scopes <span style={{ color: 'var(--text-faded)' }}>(optional — leave empty to inherit all of your permissions)</span>
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
          <div style={{ marginTop: 4, fontSize: 10, color: 'var(--text-faded)' }}>
            Scopes can only narrow — a token can never grant a permission you don't already hold.
          </div>
        </div>
      </form>

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
              <th>Scopes</th>
              <th>Expires</th>
              <th>Last used</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.length === 0 && (
              <tr><td colSpan={6} style={{ color: 'var(--text-muted)', fontSize: 12 }}>No personal tokens yet.</td></tr>
            )}
            {list.data.map((t) => {
              // time.Time zero serialises as "0001-01-01T00:00:00Z" — treat as no expiry, not expired.
              const hasExpiry = t.expires_at && new Date(t.expires_at).getFullYear() > 1000;
              const expired = hasExpiry && new Date(t.expires_at!) < new Date();
              return (
                <tr key={t.id}>
                  <td style={{ fontWeight: 500 }}>{t.name}</td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                    {t.scopes && t.scopes.length > 0
                      ? <span title={t.scopes.join(', ')}>{t.scopes.length} scope{t.scopes.length === 1 ? '' : 's'}</span>
                      : <span style={{ color: 'var(--text-faded)' }}>full (inherit)</span>}
                  </td>
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
                        onClick={async () => { if (await confirm({ title: 'Revoke API token?', body: <>Revoke <code>{t.name}</code>? Any client still using it will fail with 401 on the next call.</> })) revoke.mutate(t.id); }}
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
