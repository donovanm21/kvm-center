import { useEffect, useState } from 'react';

import { Modal } from '@/components/Modal';
import { CheckList, ErrorBanner, Field, TextArea, TextInput } from '@/components/FormBits';
import { useConfirm } from '@/hooks/useConfirm';
import {
  useCreateGroup,
  useDeleteGroup,
  useGroups,
  useRoles,
  useSetGroupMembers,
  useUpdateGroup,
  useUsers,
  type AdminGroup,
} from '@/hooks/useAdminResources';

type FormMode = { mode: 'new' } | { mode: 'edit'; group: AdminGroup };

export function GroupsTab() {
  const groups = useGroups();
  const confirmFn = useConfirm();
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
                <th style={{ width: 90 }}>Members</th>
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
                  <td style={{ color: g.member_count ? 'var(--text-body)' : 'var(--text-faded)', fontVariantNumeric: 'tabular-nums' }}>
                    {g.member_count ?? 0}
                  </td>
                  <td style={{ display: 'flex', gap: 6 }}>
                    <button className="btn-secondary-sm" onClick={() => setForm({ mode: 'edit', group: g })}>Edit</button>
                    <button
                      className="btn-danger-sm"
                      onClick={async () => {
                        if (await confirmFn({ title: 'Delete group?', body: <>Delete <code>{g.name}</code>? Members will lose their membership.</> })) del.mutate(g.id);
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
  const users = useUsers();
  const create = useCreateGroup();
  const update = useUpdateGroup();
  const setMembers = useSetGroupMembers();

  // Seed member selection from the live user list — every user whose
  // group_ids contains the current group is checked. New groups start empty.
  // Tracked separately so we can compute initial-vs-final to decide whether
  // to fire the SetGroupMembers PUT on save (skip when unchanged).
  const [memberIDs, setMemberIDs] = useState<string[]>([]);
  const [initialMembers, setInitialMembers] = useState<string[]>([]);
  const seedTarget = !isNew && existing ? existing.id : null;
  useEffect(() => {
    if (seedTarget && users.data) {
      const seeded = users.data.filter((u) => (u.group_ids ?? []).includes(seedTarget)).map((u) => u.id);
      setMemberIDs(seeded);
      setInitialMembers(seeded);
    }
  }, [seedTarget, users.data]);

  const busy = create.isPending || update.isPending || setMembers.isPending;

  const toggleRole = (id: string) =>
    setRoleIDs((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));
  const toggleMember = (id: string) =>
    setMemberIDs((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  const submit = async () => {
    setErr(null);
    if (!name.trim()) {
      setErr('Name is required.');
      return;
    }
    try {
      let groupID = existing?.id;
      if (isNew) {
        const created = await create.mutateAsync({ name: name.trim(), description: desc.trim(), role_ids: roleIDs });
        groupID = created.id;
      } else {
        await update.mutateAsync({
          id: existing!.id,
          payload: { name: name.trim(), description: desc.trim(), role_ids: roleIDs },
        });
      }
      // Skip the membership PUT when nothing changed on edit (avoids the
      // "Members updated (0/0)" no-op toast).
      const initialSet = new Set(initialMembers);
      const finalSet = new Set(memberIDs);
      const changed =
        memberIDs.length !== initialMembers.length ||
        memberIDs.some((id) => !initialSet.has(id)) ||
        initialMembers.some((id) => !finalSet.has(id));
      if (groupID && (isNew ? memberIDs.length > 0 : changed)) {
        await setMembers.mutateAsync({ id: groupID, userIDs: memberIDs });
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
          onToggle={toggleRole}
          empty="No roles defined yet."
        />
      </Field>
      <Field label={`Members (${memberIDs.length})`}>
        <CheckList
          items={(users.data ?? []).map((u) => ({
            id: u.id,
            label: u.username,
            hint: u.email || (u.enabled ? undefined : 'disabled'),
          }))}
          selected={memberIDs}
          onToggle={toggleMember}
          empty={users.isLoading ? 'Loading users…' : 'No users yet.'}
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
