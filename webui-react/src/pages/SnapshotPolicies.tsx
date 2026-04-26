import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { PaginationBar, usePagination } from '@/components/Pagination';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useVMs } from '@/hooks/useInventory';
import { useToast } from '@/hooks/useToast';
import { qk } from '@/queryKeys';

interface SnapshotPolicy {
  id?: string;
  name: string;
  enabled: boolean;
  vm_uuids: string[];
  every_minutes: number;
  retention_keep: number;
  created_at?: string;
  last_run_at?: string;
  last_run_error?: string;
  last_run_count?: number;
}

// Common presets the UI offers as a convenience — operator can always
// type a custom number of minutes in the numeric field.
const PRESETS: { label: string; minutes: number }[] = [
  { label: 'Hourly',  minutes: 60 },
  { label: '4-hourly', minutes: 240 },
  { label: 'Daily',   minutes: 1440 },
  { label: 'Weekly',  minutes: 10080 },
];

/**
 * Settings → Snapshot Policies: browse/create/edit recurring snapshot
 * rules. The server's scheduler wakes once a minute and fires policies
 * whose interval has elapsed; the retention count decides how many
 * policy-generated snapshots survive per VM. Manual snapshots are
 * never touched because retention only looks at names matching the
 * policy-<id>- prefix the server embeds.
 */
export function SnapshotPolicies() {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const features = useFeatures();
  const snapshotsDisabled = !!features.data?.disable_snapshots;
  // Flipping disable_snapshots in General must fully gate this page —
  // both the server (scheduler + CRUD) and the UI affordances. The
  // server returns 403 on any write anyway; this just stops the
  // operator's clicks from making a doomed round-trip.
  const canManage = hasPerm('vm.snapshot') && !snapshotsDisabled;
  const manageTitle = !hasPerm('vm.snapshot')
    ? 'Requires vm.snapshot'
    : snapshotsDisabled
      ? 'Snapshots are disabled in Settings → General'
      : '';

  const policies = useQuery({
    queryKey: qk.snapshotPolicies(),
    queryFn: () => apiGet<SnapshotPolicy[]>('/api/snapshot-policies'),
    refetchInterval: 60_000,
  });
  const vms = useVMs();

  const [editing, setEditing] = useState<SnapshotPolicy | null>(null);

  const newPolicy = (): SnapshotPolicy => ({
    name: '', enabled: true, vm_uuids: [], every_minutes: 1440, retention_keep: 7,
  });

  const remove = useMutation({
    mutationFn: (id: string) =>
      fetch(`/api/snapshot-policies/${encodeURIComponent(id)}`, {
        method: 'DELETE', credentials: 'same-origin',
      }).then(async (r) => { if (!r.ok) throw new Error(await r.text()); }),
    onSuccess: () => {
      toast('success', 'Policy deleted');
      qc.invalidateQueries({ queryKey: qk.snapshotPolicies() });
    },
    onError: (e) => toast('error', `Delete failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const runNow = useMutation({
    mutationFn: (id: string) => apiPost(`/api/snapshot-policies/${encodeURIComponent(id)}/run`),
    onSuccess: () => {
      toast('success', 'Policy queued — first snapshots will appear in a moment');
      qc.invalidateQueries({ queryKey: qk.snapshotPolicies() });
    },
    onError: (e) => toast('error', `Run failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  // Hooks must run every render — keep usePagination above any early
  // return or React throws a "rendered fewer hooks than expected" error
  // and the page blanks out.
  const list = policies.data ?? [];
  const pg = usePagination(list);

  if (policies.isLoading) return <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading policies…</div>;
  if (policies.error) return <div className="tone-panel danger" style={{ padding: 12, fontSize: 12 }}>Failed to load: {(policies.error as Error).message}</div>;

  return (
    <>
      {snapshotsDisabled && (
        <div className="tone-panel warn" style={{ padding: 12, fontSize: 12, marginBottom: 12 }}>
          Snapshots are disabled in <strong>Settings → General</strong>. The
          scheduler is paused and no policy can be created, edited, run, or
          deleted until the feature is re-enabled.
        </div>
      )}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16, marginBottom: 12 }}>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: 0, maxWidth: 720 }}>
          Schedule recurring snapshots per VM with retention. The server's scheduler ticks once a
          minute and fires every enabled policy whose interval has elapsed; manual snapshots are
          never touched by retention pruning.
        </p>
        <button
          type="button"
          className="btn-primary-sm"
          disabled={!canManage}
          onClick={() => setEditing(newPolicy())}
          title={manageTitle}
        >
          + New policy
        </button>
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {list.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: 'var(--text-muted)' }}>
            No snapshot policies yet. Create one to schedule automatic snapshots with retention.
          </div>
        ) : (
          <>
          <div className="data-table-scroll">
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Enabled</th>
                <th>VMs</th>
                <th>Every</th>
                <th>Keep</th>
                <th>Last run</th>
                <th style={{ width: 160 }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {pg.items.map((p) => (
                <tr key={p.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{p.name}</td>
                  <td><span className={`tone-pill ${p.enabled ? 'ok' : 'muted'}`}>{p.enabled ? 'enabled' : 'disabled'}</span></td>
                  <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{p.vm_uuids.length}</td>
                  <td style={{ fontSize: 12 }}>{humanInterval(p.every_minutes)}</td>
                  <td style={{ fontSize: 12 }}>{p.retention_keep}</td>
                  <td>
                    {p.last_run_at ? (
                      <>
                        <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>{new Date(p.last_run_at).toLocaleString()}</div>
                        {p.last_run_error ? (
                          <div className="tone-pill danger" style={{ marginTop: 4, maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', padding: '2px 8px' }} title={p.last_run_error}>{p.last_run_error}</div>
                        ) : (
                          <div style={{ fontSize: 10, color: 'var(--text-faded)' }}>{p.last_run_count ?? 0} snapshot{p.last_run_count === 1 ? '' : 's'}</div>
                        )}
                      </>
                    ) : (
                      <span style={{ fontSize: 11, color: 'var(--text-faded)' }}>—</span>
                    )}
                  </td>
                  <td>
                    <div style={{ display: 'flex', gap: 6 }}>
                      <button type="button" className="btn-secondary-sm" disabled={!canManage} onClick={() => setEditing(p)}>Edit</button>
                      <button type="button" className="btn-secondary-sm" disabled={!canManage} onClick={() => p.id && runNow.mutate(p.id)}>Run now</button>
                      <button type="button" className="btn-danger-sm" disabled={!canManage} onClick={() => p.id && confirm(`Delete policy "${p.name}"?`) && remove.mutate(p.id)}>Delete</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <PaginationBar pagination={pg} noun="policies" />
          </>
        )}
      </div>

      {editing && (
        <PolicyEditor
          initial={editing}
          vms={(vms.data ?? []).map((v) => ({ uuid: v.uuid, name: v.name, host: v.host_id }))}
          onClose={() => setEditing(null)}
          onSaved={() => qc.invalidateQueries({ queryKey: qk.snapshotPolicies() })}
        />
      )}
    </>
  );
}

function humanInterval(m: number): string {
  if (m % 10080 === 0) return `${m / 10080} week${m === 10080 ? '' : 's'}`;
  if (m % 1440 === 0) return `${m / 1440} day${m === 1440 ? '' : 's'}`;
  if (m % 60 === 0) return `${m / 60} hour${m === 60 ? '' : 's'}`;
  return `${m} min`;
}

// ── Editor modal ────────────────────────────────────────────────────

function PolicyEditor({
  initial, vms, onClose, onSaved,
}: {
  initial: SnapshotPolicy;
  vms: { uuid: string; name: string; host: string }[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { toast } = useToast();
  const [form, setForm] = useState<SnapshotPolicy>(initial);
  const update = (patch: Partial<SnapshotPolicy>) => setForm({ ...form, ...patch });
  const isEdit = Boolean(initial.id);

  const save = useMutation({
    mutationFn: async () => {
      const url = isEdit ? `/api/snapshot-policies/${encodeURIComponent(initial.id!)}` : '/api/snapshot-policies';
      const method = isEdit ? 'PUT' : 'POST';
      const r = await fetch(url, {
        method, credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      });
      if (!r.ok) throw new Error(await r.text());
      return r.json();
    },
    onSuccess: () => {
      toast('success', `Policy ${isEdit ? 'updated' : 'created'}`);
      onSaved();
      onClose();
    },
    onError: (e) => toast('error', `Save failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const [filter, setFilter] = useState('');
  const filteredVMs = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return vms
      .filter((v) => !q || v.name.toLowerCase().includes(q) || v.host.toLowerCase().includes(q))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [vms, filter]);

  const toggleVM = (uuid: string) => {
    const s = new Set(form.vm_uuids);
    if (s.has(uuid)) s.delete(uuid); else s.add(uuid);
    update({ vm_uuids: Array.from(s) });
  };

  return (
    <div style={backdrop} onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div style={modal}>
        <h2 style={{ margin: '0 0 14px 0' }}>{isEdit ? 'Edit policy' : 'New snapshot policy'}</h2>

        <Field label="Name">
          <input type="text" value={form.name} onChange={(e) => update({ name: e.target.value })} style={input} placeholder="e.g. Daily production snapshots" />
        </Field>

        <Field label="Schedule">
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {PRESETS.map((p) => (
              <button
                key={p.minutes}
                type="button"
                className="btn-secondary-sm"
                aria-pressed={form.every_minutes === p.minutes}
                onClick={() => update({ every_minutes: p.minutes })}
                style={form.every_minutes === p.minutes
                  ? { borderColor: 'var(--accent)', color: 'var(--accent)' }
                  : undefined}
              >{p.label}</button>
            ))}
          </div>
          <div style={{ marginTop: 8 }}>
            Every&nbsp;
            <input type="number" min={1} value={form.every_minutes} onChange={(e) => update({ every_minutes: parseInt(e.target.value, 10) || 60 })} style={{ ...input, width: 90, display: 'inline-block' }} />
            &nbsp;minutes
            <span style={{ marginLeft: 10, color: 'var(--text-faded)', fontSize: 11 }}>({humanInterval(form.every_minutes)})</span>
          </div>
        </Field>

        <Field label="Retention (keep last N policy-generated snapshots per VM)">
          <input type="number" min={1} max={100} value={form.retention_keep} onChange={(e) => update({ retention_keep: parseInt(e.target.value, 10) || 1 })} style={{ ...input, width: 90 }} />
        </Field>

        <Field label={`VMs (${form.vm_uuids.length} selected)`}>
          <input type="search" placeholder="Filter VMs…" value={filter} onChange={(e) => setFilter(e.target.value)} style={input} />
          <div style={{
            marginTop: 6,
            maxHeight: 220, overflowY: 'auto',
            border: '1px solid var(--border)', borderRadius: 4,
            background: 'var(--bg-input)',
          }}>
            {filteredVMs.length === 0 ? (
              <div style={{ padding: 10, fontSize: 11, color: 'var(--text-muted)' }}>No VMs match.</div>
            ) : (
              filteredVMs.map((v) => (
                <label key={v.uuid} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', fontSize: 12, cursor: 'pointer', borderBottom: '1px solid var(--border-subtle)' }}>
                  <input type="checkbox" checked={form.vm_uuids.includes(v.uuid)} onChange={() => toggleVM(v.uuid)} />
                  <span style={{ color: 'var(--text-body)' }}>{v.name}</span>
                  <span style={{ flex: 1 }} />
                  <span style={{ fontSize: 10, color: 'var(--text-faded)', fontFamily: "'SF Mono', monospace" }}>{v.host}</span>
                </label>
              ))
            )}
          </div>
        </Field>

        <Field label="Enabled">
          <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <input type="checkbox" checked={form.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
            <span style={{ fontSize: 12 }}>Fire this policy on schedule</span>
          </label>
        </Field>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 }}>
          <button type="button" className="btn-secondary-sm" onClick={onClose}>Cancel</button>
          <button
            type="button"
            className="btn-primary-sm"
            disabled={save.isPending || !form.name || form.vm_uuids.length === 0 || form.every_minutes <= 0}
            onClick={() => save.mutate()}
          >
            {save.isPending ? 'Saving…' : isEdit ? 'Save' : 'Create policy'}
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 11, color: 'var(--text-faded)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 4 }}>{label}</div>
      {children}
    </div>
  );
}

const backdrop: React.CSSProperties = {
  position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', zIndex: 100,
  display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 20,
};
const modal: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 8, padding: 20, width: 560, maxWidth: '100%',
  maxHeight: '90vh', overflowY: 'auto',
};
const input: React.CSSProperties = {
  width: '100%', height: 32, padding: '0 10px',
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontSize: 12, fontFamily: 'inherit',
};
