import { useState } from 'react';

import { Modal } from '@/components/Modal';
import {
  useCreateUser,
  useDeleteUser,
  useGroups,
  useRoles,
  useUpdateUser,
  useUsers,
  type AdminUser,
} from '@/hooks/useAdminResources';

type FormMode = { mode: 'new' } | { mode: 'edit'; user: AdminUser };

export function UsersTab() {
  const users = useUsers();
  const roles = useRoles();
  const groups = useGroups();
  const del = useDeleteUser();
  const [form, setForm] = useState<FormMode | null>(null);

  const roleName = (id: string) => roles.data?.roles.find((r) => r.id === id)?.name ?? id;
  const groupName = (id: string) => groups.data?.find((g) => g.id === id)?.name ?? id;

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn-primary-sm" onClick={() => setForm({ mode: 'new' })}>+ Add User</button>
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {users.isLoading && <Empty>Loading…</Empty>}
        {users.error && <Empty danger>Failed to load users: {(users.error as Error).message}</Empty>}
        {users.data && users.data.length === 0 && !users.isLoading && <Empty>No users yet.</Empty>}
        {users.data && users.data.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <th>Username</th>
                <th>Email</th>
                <th>Roles</th>
                <th>Groups</th>
                <th>Status</th>
                <th style={{ width: 160 }} />
              </tr>
            </thead>
            <tbody>
              {users.data.map((u) => (
                <tr key={u.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>
                    {u.username}
                    {u.totp_enabled && <span className="tone-pill ok" style={totpBadge} title="TOTP 2FA enrolled">2FA</span>}
                    {u.external_ids && u.external_ids.length > 0 && (
                      <span className="tone-pill warn" style={extBadge} title="Federated identity">ext</span>
                    )}
                  </td>
                  <td style={{ color: 'var(--text-muted)' }}>{u.email || '—'}</td>
                  <td>
                    <Pills values={u.role_ids ?? []} label={roleName} />
                  </td>
                  <td>
                    <Pills values={u.group_ids ?? []} label={groupName} />
                  </td>
                  <td>
                    <span className={`state-pill ${u.enabled ? 'running' : 'stopped'}`}>
                      {u.enabled ? 'active' : 'disabled'}
                    </span>
                  </td>
                  <td style={{ display: 'flex', gap: 6 }}>
                    <button className="btn-secondary-sm" onClick={() => setForm({ mode: 'edit', user: u })}>
                      Edit
                    </button>
                    <button
                      className="btn-danger-sm"
                      onClick={() => {
                        if (confirm(`Delete user "${u.username}"?`)) del.mutate(u.id);
                      }}
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {form && <UserFormModal form={form} onClose={() => setForm(null)} />}
    </>
  );
}

function Pills({ values, label }: { values: string[]; label: (id: string) => string }) {
  if (!values.length) return <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>none</span>;
  return (
    <>
      {values.map((v) => (
        <span key={v} style={pillStyle}>{label(v)}</span>
      ))}
    </>
  );
}

function Empty({ children, danger = false }: { children: React.ReactNode; danger?: boolean }) {
  return (
    <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: danger ? '#F85149' : 'var(--text-muted)' }}>
      {children}
    </div>
  );
}

/* ── Add / Edit modal ───────────────────────────────────── */
function UserFormModal({ form, onClose }: { form: FormMode; onClose: () => void }) {
  const isNew = form.mode === 'new';
  const existing = isNew ? undefined : form.user;

  const [username, setUsername] = useState(existing?.username ?? '');
  const [email, setEmail] = useState(existing?.email ?? '');
  const [password, setPassword] = useState('');
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [roleIDs, setRoleIDs] = useState<string[]>(existing?.role_ids ?? []);
  const [groupIDs, setGroupIDs] = useState<string[]>(existing?.group_ids ?? []);
  const [err, setErr] = useState<string | null>(null);

  const roles = useRoles();
  const groups = useGroups();
  const create = useCreateUser();
  const update = useUpdateUser();

  const busy = create.isPending || update.isPending;

  const toggle = (list: string[], id: string, setter: (v: string[]) => void) =>
    setter(list.includes(id) ? list.filter((x) => x !== id) : [...list, id]);

  const submit = async () => {
    setErr(null);
    if (isNew) {
      if (!username.trim() || !password) {
        setErr('Username and password are required.');
        return;
      }
      if (password.length < 8) {
        setErr('Password must be at least 8 characters.');
        return;
      }
      try {
        await create.mutateAsync({
          username: username.trim(),
          email: email.trim(),
          password,
          enabled,
          role_ids: roleIDs,
          group_ids: groupIDs,
        });
        onClose();
      } catch {
        /* toast shown by hook */
      }
      return;
    }
    try {
      const payload: Parameters<typeof update.mutateAsync>[0]['payload'] = {
        email: email.trim(),
        enabled,
        role_ids: roleIDs,
        group_ids: groupIDs,
      };
      if (password) payload.password = password;
      await update.mutateAsync({ id: existing!.id, payload });
      onClose();
    } catch {
      /* toast shown by hook */
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={isNew ? 'New User' : `Edit User — ${existing?.username}`}
      width={520}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={busy}>
            {busy ? 'Saving…' : isNew ? 'Create User' : 'Save'}
          </button>
        </>
      }
    >
      {isNew && (
        <Field label="Username">
          <input style={inputStyle} autoFocus value={username} onChange={(e) => setUsername(e.target.value)} />
        </Field>
      )}
      <Field label="Email">
        <input type="email" style={inputStyle} value={email} onChange={(e) => setEmail(e.target.value)} />
      </Field>
      <Field label={isNew ? 'Password (min 8 characters)' : 'Password (leave blank to keep current)'}>
        <input
          type="password"
          style={inputStyle}
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </Field>

      <Field label="Roles">
        <CheckList
          items={(roles.data?.roles ?? []).map((r) => ({ id: r.id, label: r.name }))}
          selected={roleIDs}
          onToggle={(id) => toggle(roleIDs, id, setRoleIDs)}
          empty="No roles defined yet."
        />
      </Field>
      <Field label="Groups">
        <CheckList
          items={(groups.data ?? []).map((g) => ({ id: g.id, label: g.name }))}
          selected={groupIDs}
          onToggle={(id) => toggle(groupIDs, id, setGroupIDs)}
          empty="No groups defined yet."
        />
      </Field>

      <label style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        <span style={{ fontSize: 12, color: 'var(--text-body)' }}>User is enabled (can sign in)</span>
      </label>

      {err && (
        <div className="tone-panel danger" style={{ marginTop: 10, padding: '8px 12px', borderRadius: 4, fontSize: 12 }}>
          {err}
        </div>
      )}
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginTop: 10 }}>
      <label style={fieldLabelStyle}>{label}</label>
      {children}
    </div>
  );
}

function CheckList({
  items,
  selected,
  onToggle,
  empty,
}: {
  items: { id: string; label: string }[];
  selected: string[];
  onToggle: (id: string) => void;
  empty: string;
}) {
  if (items.length === 0) {
    return (
      <div
        style={{
          padding: '10px 12px',
          background: 'var(--bg-input)',
          border: '1px solid var(--border)',
          borderRadius: 4,
          fontSize: 11,
          color: 'var(--text-faded)',
        }}
      >
        {empty}
      </div>
    );
  }
  return (
    <div
      style={{
        maxHeight: 140,
        overflowY: 'auto',
        border: '1px solid var(--border)',
        borderRadius: 4,
        padding: '8px 12px',
        background: 'var(--bg-input)',
      }}
    >
      {items.map((it) => (
        <label key={it.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0', cursor: 'pointer' }}>
          <input type="checkbox" checked={selected.includes(it.id)} onChange={() => onToggle(it.id)} />
          <span style={{ fontSize: 12, color: 'var(--text-body)' }}>{it.label}</span>
        </label>
      ))}
    </div>
  );
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  height: 34,
  padding: '0 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
};
const fieldLabelStyle: React.CSSProperties = {
  display: 'block',
  fontSize: 11,
  color: 'var(--text-muted)',
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  marginBottom: 6,
};
const pillStyle: React.CSSProperties = {
  display: 'inline-block',
  padding: '2px 8px',
  background: 'var(--surface-3)',
  borderRadius: 10,
  fontSize: 10,
  marginRight: 4,
  color: 'var(--text-body)',
};
// Compact badge wrappers — give the tone via className, let CSS handle
// both themes. Kept around as CSSProperties only for the margin nudges.
const totpBadge: React.CSSProperties = { marginLeft: 8 };
const extBadge:  React.CSSProperties = { marginLeft: 6 };
