import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';

import { Modal } from '@/components/Modal';
import { Field, TextInput } from '@/components/FormBits';
import { useVMs } from '@/hooks/useInventory';
import { useConfirm } from '@/hooks/useConfirm';
import {
  useCreateSnapshot,
  useDeleteSnapshot,
  useRevertSnapshot,
  useSnapshots,
} from '@/hooks/useSnapshots';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures } from '@/hooks/useInventory';
import type { VM } from '@/types';

/**
 * Fleet-wide Snapshots view: two-column layout with the list of VMs
 * that have at least one snapshot on the left, and the selected VM's
 * snapshot detail on the right. Mirrors the v3 behaviour so users who
 * came from Snapshots in v3 land somewhere familiar.
 */
export function SnapshotsOverview() {
  const vms = useVMs();
  const { uuid } = useParams<{ uuid: string }>();
  const [query, setQuery] = useState('');

  // Stable sort (host_id, then vm name) so successive telemetry
  // ticks don't reorder the list. Without this, the server's map
  // iteration order on vms[hostID] shuffles per tick and the list
  // "bounces" — e.g. Web and TestVM swap positions every few
  // seconds. v5.33.3 fix.
  const vmsWithSnaps = useMemo(
    () =>
      (vms.data ?? [])
        .filter((v) => (v.snapshot_count ?? 0) > 0)
        .slice()
        .sort((a, b) => {
          const ha = (a.host_id ?? '').toLowerCase();
          const hb = (b.host_id ?? '').toLowerCase();
          if (ha !== hb) return ha < hb ? -1 : 1;
          const na = (a.name ?? '').toLowerCase();
          const nb = (b.name ?? '').toLowerCase();
          if (na !== nb) return na < nb ? -1 : 1;
          return 0;
        }),
    [vms.data],
  );
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return vmsWithSnaps;
    return vmsWithSnaps.filter((v) => v.name.toLowerCase().includes(q) || v.host_id.toLowerCase().includes(q));
  }, [vmsWithSnaps, query]);

  const selected = uuid ? (vms.data ?? []).find((v) => v.uuid === uuid) : null;

  return (
    <div className="mobile-stack" style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 16, alignItems: 'flex-start' }}>
      <div className="panel" style={{ padding: 0, overflow: 'hidden', minHeight: 200 }}>
        <div style={listHeaderStyle}>
          VMs with Snapshots
        </div>
        <div style={{ padding: 8 }}>
          <input
            type="search"
            placeholder="Filter…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            style={searchStyle}
          />
        </div>
        {filtered.length === 0 ? (
          <div style={{ padding: 18, fontSize: 12, color: 'var(--text-muted)' }}>
            {vmsWithSnaps.length === 0 ? 'No VMs have snapshots yet.' : 'No VMs match your filter.'}
          </div>
        ) : (
          <div>
            {filtered.map((v) => (
              <TreeRow key={v.uuid} vm={v} active={v.uuid === uuid} />
            ))}
          </div>
        )}
      </div>

      <div>
        {selected ? (
          <SnapshotDetail vm={selected} />
        ) : (
          <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>
            Pick a VM on the left to view and manage its snapshots.
          </div>
        )}
      </div>
    </div>
  );
}

function TreeRow({ vm, active }: { vm: VM; active: boolean }) {
  return (
    <Link
      to={`/snapshots/vm/${encodeURIComponent(vm.uuid)}`}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '10px 14px',
        borderLeft: `3px solid ${active ? 'var(--accent)' : 'transparent'}`,
        background: active ? 'var(--accent-subtle)' : 'transparent',
        textDecoration: 'none',
        cursor: 'pointer',
      }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            fontWeight: 500,
            fontSize: 12,
            color: active ? 'var(--accent-hover)' : 'var(--text-strong)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {vm.name}
        </div>
        <div style={{ fontSize: 10, color: 'var(--text-faded)' }}>{vm.host_id}</div>
      </div>
      <span
        style={{
          padding: '2px 8px',
          fontSize: 10,
          borderRadius: 10,
          background: 'var(--surface-3)',
          color: 'var(--text-body)',
        }}
      >
        {vm.snapshot_count}
      </span>
    </Link>
  );
}

function SnapshotDetail({ vm }: { vm: VM }) {
  const snaps = useSnapshots(vm.host_id, vm.name);
  const revert = useRevertSnapshot();
  const del = useDeleteSnapshot();
  const confirmFn = useConfirm();
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const nav = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const hasPermSnap = hasPerm('vm.snapshot') && !features.data?.disable_snapshots;
  // Agent-detected libvirt incompatibility (e.g. UEFI + raw nvram) —
  // we can't even try to take a snapshot, so disable the button and
  // surface the reason.
  const blocked = vm.snapshots_blocked_reason || '';
  const canSnap = hasPermSnap && !blocked;
  const createTitle = blocked
    ? blocked
    : !hasPermSnap
      ? 'Requires vm.snapshot or feature disabled'
      : 'Create a new snapshot of this VM';

  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          padding: '14px 18px',
          borderBottom: '1px solid var(--border)',
        }}
      >
        <div>
          <div style={{ fontSize: 16, fontWeight: 600, color: 'var(--text-strong)' }}>{vm.name}</div>
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 2, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            <span>{vm.host_id} · <span className={`state-pill ${vm.state}`}>{vm.state}</span></span>
            {blocked && (
              <span
                className="tone-pill warn"
                style={{ fontSize: 10 }}
                title={blocked}
              >
                ⚠ Snapshots unsupported
              </span>
            )}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 6 }}>
          <button
            type="button"
            className="btn-primary-sm"
            onClick={() => setCreateOpen(true)}
            disabled={!canSnap}
            title={createTitle}
          >
            📷 Create Snapshot
          </button>
          <button
            type="button"
            className="btn-secondary-sm"
            onClick={() => snaps.refetch()}
            disabled={snaps.isFetching}
          >
            ↻ Refresh
          </button>
          <button
            type="button"
            className="btn-secondary-sm"
            onClick={() => nav(`/vms/${encodeURIComponent(vm.uuid)}/snapshots`)}
          >
            Open in VM view →
          </button>
        </div>
      </div>

      {snaps.isLoading && <div style={{ padding: 18, fontSize: 12, color: 'var(--text-muted)' }}>Loading snapshots…</div>}
      {snaps.error && (
        <div style={{ padding: 18, fontSize: 12, color: '#F85149' }}>
          Failed to load: {(snaps.error as Error).message}
        </div>
      )}
      {snaps.data && snaps.data.length === 0 && !snaps.isLoading && (
        <div style={{ padding: 18, fontSize: 12, color: 'var(--text-muted)' }}>
          No snapshots (was {vm.snapshot_count} on last telemetry tick — the list may still be loading).
        </div>
      )}

      {snaps.data && snaps.data.length > 0 && (
        <table className="data-table">
          <thead>
            <tr>
              <th>Snapshot</th>
              <th>Created</th>
              <th>State</th>
              <th>Parent</th>
              <th style={{ width: 180 }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {snaps.data.map((s) => (
              <tr key={s.name}>
                <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>
                  {s.name}
                  {s.is_current && (
                    <span className="tone-pill warn" style={currentPillStyle}>Current</span>
                  )}
                </td>
                <td style={{ fontFamily: "'SF Mono', monospace", color: 'var(--text-muted)', fontSize: 11 }}>
                  {s.creation_time}
                </td>
                <td><span className={`state-pill ${s.state === 'running' ? 'running' : 'stopped'}`}>{s.state}</span></td>
                <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{s.parent || '—'}</td>
                <td>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button
                      className="btn-secondary-sm"
                      disabled={s.is_current || !canSnap}
                      onClick={() => revert.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name })}
                      title={!canSnap ? 'Requires vm.snapshot' : s.is_current ? 'Already current' : 'Revert the VM to this snapshot'}
                    >
                      <RevertIcon /> Revert
                    </button>
                    <button
                      className="btn-danger-sm"
                      disabled={!canSnap}
                      onClick={async () => {
                        if (await confirmFn({ title: 'Delete snapshot?', body: <>Delete <code>{s.name}</code> from <strong>{vm.name}</strong>? The disk-state at that snapshot is permanently lost.</> })) {
                          del.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name });
                        }
                      }}
                    >
                      <TrashIcon /> Delete
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <CreateSnapshotModal vm={vm} open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}

function CreateSnapshotModal({ vm, open, onClose }: { vm: VM; open: boolean; onClose: () => void }) {
  const [name, setName] = useState(defaultSnapshotName());
  const create = useCreateSnapshot();
  const submit = async () => {
    if (!name.trim()) return;
    await create.mutateAsync({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: name.trim() });
    onClose();
    setName(defaultSnapshotName());
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Create Snapshot — ${vm.name}`}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={!name.trim() || create.isPending}>
            {create.isPending ? 'Creating…' : 'Create Snapshot'}
          </button>
        </>
      }
    >
      <Field label="Name">
        <TextInput
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          style={{ fontFamily: "'SF Mono', monospace" }}
        />
      </Field>
      <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text-faded)' }}>
        Snapshots only work on VMs with qcow2 disks.
      </div>
    </Modal>
  );
}

function RevertIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
      <path d="M8 3a5 5 0 1 0 4.546 2.914.5.5 0 0 1 .908-.417A6 6 0 1 1 8 2v1z" />
      <path d="M8 4.466V.534a.25.25 0 0 0-.41-.192L5.23 2.308a.25.25 0 0 0 0 .384l2.36 1.966A.25.25 0 0 0 8 4.466z" />
    </svg>
  );
}
function TrashIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
      <path d="M5.5 5.5A.5.5 0 0 1 6 6v6a.5.5 0 0 1-1 0V6a.5.5 0 0 1 .5-.5zm2.5 0a.5.5 0 0 1 .5.5v6a.5.5 0 0 1-1 0V6a.5.5 0 0 1 .5-.5zm3 .5a.5.5 0 0 0-1 0v6a.5.5 0 0 0 1 0V6z" />
      <path fillRule="evenodd" d="M14.5 3a1 1 0 0 1-1 1H13v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V4h-.5a1 1 0 0 1-1-1V2a1 1 0 0 1 1-1H6a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1h3.5a1 1 0 0 1 1 1v1zM4.118 4 4 4.059V13a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1V4.059L11.882 4zM2.5 3V2h11v1z" />
    </svg>
  );
}

function defaultSnapshotName(): string {
  const d = new Date();
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `snap-${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}`;
}

const listHeaderStyle: React.CSSProperties = {
  padding: '12px 14px',
  borderBottom: '1px solid var(--border)',
  fontSize: 11,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  color: 'var(--text-faded)',
};
const searchStyle: React.CSSProperties = {
  width: '100%',
  height: 30,
  padding: '0 10px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
};
const currentPillStyle: React.CSSProperties = { marginLeft: 8 };
