import { useState } from 'react';

import { Modal } from '@/components/Modal';
import { CheckList, ErrorBanner, Field, TextArea, TextInput } from '@/components/FormBits';
import {
  useCreateGroup,
  useDeleteGroup,
  useGroups,
  useRoles,
  useUpdateGroup,
  type AdminGroup,
} from '@/hooks/useAdminResources';

type FormMode = { mode: 'new' } | { mode: 'edit'; group: AdminGroup };

export function GroupsTab() {
  const groups = useGroups();
  const roles = useRoles();
  const del = useDeleteGroup();
  const [form, setForm] = useState<FormMode | null>(null);
  const roleName = (id: string) => roles.data?.roles.find((r) => r.id === id)?.name ?? id;

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn-primary-sm" onClick={() => setForm({ mode: 'new' })}>+ Add Group</button>
      </div>
      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {groups.isLoading && <Empty>Loading…</Empty>}
        {groups.error && <Empty danger>Failed: {(groups.error as Error).message}</Empty>}
        {groups.data && groups.data.length === 0 && !groups.isLoading && <Empty>No groups yet.</Empty>}
        {groups.data && groups.data.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Description</th>
                <th>Roles</th>
                <th style={{ width: 160 }} />
              </tr>
            </thead>
            <tbody>
              {groups.data.map((g) => (
                <tr key={g.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{g.name}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{g.description || '—'}</td>
                  <td>
                    {(g.role_ids ?? []).length === 0 ? (
                      <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>none</span>
                    ) : (
                      (g.role_ids ?? []).map((rid) => (
                        <span key={rid} style={pillStyle}>{roleName(rid)}</span>
                      ))
                    )}
                  </td>
                  <td style={{ display: 'flex', gap: 6 }}>
                    <button className="btn-secondary-sm" onClick={() => setForm({ mode: 'edit', group: g })}>Edit</button>
                    <button
                      className="btn-danger-sm"
                      onClick={() => {
                        if (confirm(`Delete group "${g.name}"? Members will lose their membership.`)) del.mutate(g.id);
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
      {form && <GroupFormModal form={form} onClose={() => setForm(null)} />}
    </>
  );
}

function GroupFormModal({ form, onClose }: { form: FormMode; onClose: () => void }) {
  const isNew = form.mode === 'new';
  const existing = isNew ? undefined : form.group;
  const [name, setName] = useState(existing?.name ?? '');
  const [desc, setDesc] = useState(existing?.description ?? '');
  const [roleIDs, setRoleIDs] = useState<string[]>(existing?.role_ids ?? []);
  const [err, setErr] = useState<string | null>(null);

  const roles = useRoles();
  const create = useCreateGroup();
  const update = useUpdateGroup();
  const busy = create.isPending || update.isPending;

  const toggle = (id: string) =>
    setRoleIDs((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  const submit = async () => {
    setErr(null);
    if (!name.trim()) {
      setErr('Name is required.');
      return;
    }
    try {
      if (isNew) {
        await create.mutateAsync({ name: name.trim(), description: desc.trim(), role_ids: roleIDs });
      } else {
        await update.mutateAsync({
          id: existing!.id,
          payload: { name: name.trim(), description: desc.trim(), role_ids: roleIDs },
        });
      }
      onClose();
    } catch {
      /* toast from hook */
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={isNew ? 'New Group' : `Edit Group — ${existing?.name}`}
      width={520}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={busy}>
            {busy ? 'Saving…' : isNew ? 'Create Group' : 'Save'}
          </button>
        </>
      }
    >
      <Field label="Name">
        <TextInput autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Description (optional)">
        <TextArea value={desc} onChange={(e) => setDesc(e.target.value)} rows={2} />
      </Field>
      <Field label="Roles granted to members">
        <CheckList
          items={(roles.data?.roles ?? []).map((r) => ({ id: r.id, label: r.name, hint: r.description }))}
          selected={roleIDs}
          onToggle={toggle}
          empty="No roles defined yet."
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

const pillStyle: React.CSSProperties = {
  display: 'inline-block',
  padding: '2px 8px',
  background: 'var(--surface-3)',
  borderRadius: 10,
  fontSize: 10,
  marginRight: 4,
  color: 'var(--text-body)',
};
