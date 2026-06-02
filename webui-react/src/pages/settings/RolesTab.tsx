import { useState } from 'react';

import { Modal } from '@/components/Modal';
import { CheckList, ErrorBanner, Field, TextArea, TextInput } from '@/components/FormBits';
import { useConfirm } from '@/hooks/useConfirm';
import {
  useCreateRole,
  useDeleteRole,
  useRoles,
  useUpdateRole,
  type AdminRole,
} from '@/hooks/useAdminResources';

type FormMode = { mode: 'new' } | { mode: 'edit'; role: AdminRole };

export function RolesTab() {
  const roles = useRoles();
  const confirmFn = useConfirm();
  const del = useDeleteRole();
  const [form, setForm] = useState<FormMode | null>(null);

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn-primary-sm" onClick={() => setForm({ mode: 'new' })}>+ Add Role</button>
      </div>
      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {roles.isLoading && <Empty>Loading…</Empty>}
        {roles.error && <Empty danger>Failed: {(roles.error as Error).message}</Empty>}
        {roles.data && roles.data.roles.length === 0 && !roles.isLoading && <Empty>No roles defined.</Empty>}
        {roles.data && roles.data.roles.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Description</th>
                <th>Permissions</th>
                <th style={{ width: 200 }} />
              </tr>
            </thead>
            <tbody>
              {(Array.isArray(roles.data?.roles) ? roles.data.roles : []).map((r) => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>
                    {r.name}
                    {r.built_in && <span style={builtInBadge} title="Built-in role — managed by the server">built-in</span>}
                  </td>
                  <td style={{ color: 'var(--text-muted)' }}>{r.description || '—'}</td>
                  <td>
                    {r.permissions.length === 0 ? (
                      <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>none</span>
                    ) : (
                      r.permissions.map((p) => (
                        <span key={p} style={{ ...permPillStyle, ...(p === 'admin' ? adminPillStyle : {}) }}>
                          {p}
                        </span>
                      ))
                    )}
                  </td>
                  <td style={{ display: 'flex', gap: 6 }}>
                    <button className="btn-secondary-sm" onClick={() => setForm({ mode: 'edit', role: r })}>
                      {r.built_in ? 'View' : 'Edit'}
                    </button>
                    <button
                      className="btn-danger-sm"
                      disabled={r.built_in}
                      title={r.built_in ? 'Built-in roles cannot be deleted' : ''}
                      onClick={async () => {
                        if (r.built_in) return;
                        if (await confirmFn({ title: 'Delete role?', body: <>Delete <code>{r.name}</code>? Users and groups carrying this role will lose its permissions.</> })) del.mutate(r.id);
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
      {form && <RoleFormModal form={form} onClose={() => setForm(null)} />}
    </>
  );
}

function RoleFormModal({ form, onClose }: { form: FormMode; onClose: () => void }) {
  const isNew = form.mode === 'new';
  const existing = isNew ? undefined : form.role;
  const readOnly = existing?.built_in ?? false;

  const [name, setName] = useState(existing?.name ?? '');
  const [desc, setDesc] = useState(existing?.description ?? '');
  const [perms, setPerms] = useState<string[]>(existing?.permissions ?? []);
  const [err, setErr] = useState<string | null>(null);

  const roles = useRoles();
  const create = useCreateRole();
  const update = useUpdateRole();
  const busy = create.isPending || update.isPending;

  const togglePerm = (p: string) =>
    setPerms((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]));

  const submit = async () => {
    setErr(null);
    if (readOnly) {
      onClose();
      return;
    }
    if (!name.trim()) {
      setErr('Name is required.');
      return;
    }
    try {
      if (isNew) {
        await create.mutateAsync({ name: name.trim(), description: desc.trim(), permissions: perms });
      } else {
        await update.mutateAsync({
          id: existing!.id,
          payload: { name: name.trim(), description: desc.trim(), permissions: perms },
        });
      }
      onClose();
    } catch {
      /* toast from hook */
    }
  };

  const permItems = (roles.data?.permissions ?? []).map((p) => ({
    id: p,
    label: p,
    hint: p === 'admin' ? 'wildcard — grants everything' : undefined,
  }));

  return (
    <Modal
      open
      onClose={onClose}
      title={isNew ? 'New Role' : `${readOnly ? 'View' : 'Edit'} Role — ${existing?.name}`}
      width={540}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose} disabled={busy}>
            {readOnly ? 'Close' : 'Cancel'}
          </button>
          {!readOnly && (
            <button className="btn-primary-sm" onClick={submit} disabled={busy}>
              {busy ? 'Saving…' : isNew ? 'Create Role' : 'Save'}
            </button>
          )}
        </>
      }
    >
      {readOnly && (
        <div style={builtInNote}>
          This is a built-in role — permissions are managed by the server and can't be edited from
          the UI. Assign it to users or groups via their edit dialogs.
        </div>
      )}
      <Field label="Name">
        <TextInput autoFocus={!readOnly} value={name} disabled={readOnly} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Description (optional)">
        <TextArea value={desc} disabled={readOnly} onChange={(e) => setDesc(e.target.value)} rows={2} />
      </Field>
      <Field label="Permissions">
        <CheckList
          items={permItems}
          selected={perms}
          onToggle={readOnly ? () => { /* locked */ } : togglePerm}
          empty="Server reported no permissions."
          maxHeight={220}
        />
      </Field>
      <ErrorBanner message={err} />
    </Modal>
  );
}

function Empty({ children, danger = false }: { children: React.ReactNode; danger?: boolean }) {
  return (
    <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: danger ? '#F85149' : 'var(--text-muted)' }}>
      {children}
    </div>
  );
}

const permPillStyle: React.CSSProperties = {
  display: 'inline-block',
  padding: '2px 8px',
  background: 'var(--surface-3)',
  borderRadius: 10,
  fontSize: 10,
  marginRight: 4,
  marginBottom: 3,
  color: 'var(--text-body)',
  fontFamily: "'SF Mono', monospace",
};
const adminPillStyle: React.CSSProperties = {
  background: 'var(--accent-subtle)',
  color: 'var(--accent)',
  fontWeight: 600,
};
const builtInBadge: React.CSSProperties = {
  marginLeft: 8,
  fontSize: 9,
  padding: '1px 6px',
  borderRadius: 10,
  background: 'var(--surface-3)',
  color: 'var(--text-muted)',
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
};
const builtInNote: React.CSSProperties = {
  marginBottom: 10,
  padding: '10px 12px',
  fontSize: 11,
  color: 'var(--text-muted)',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderLeft: '3px solid var(--accent)',
  borderRadius: 4,
  lineHeight: 1.5,
};
