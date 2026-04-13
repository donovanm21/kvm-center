import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';

import { Modal } from '@/components/Modal';
import { Field, TextInput } from '@/components/FormBits';
import { useVMs } from '@/hooks/useInventory';
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

  const vmsWithSnaps = useMemo(
    () => (vms.data ?? []).filter((v) => (v.snapshot_count ?? 0) > 0),
    [vms.data],
  );
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return vmsWithSnaps;
    return vmsWithSnaps.filter((v) => v.name.toLowerCase().includes(q) || v.host_id.toLowerCase().includes(q));
  }, [vmsWithSnaps, query]);

  const selected = uuid ? (vms.data ?? []).find((v) => v.uuid === uuid) : null;

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 16, alignItems: 'flex-start' }}>
      <div className="panel" style={{ padding: 0, overflow: 'hidden', minHeight: 200 }}>
        <div style={listHeaderStyle}>
          VMs with Snapshots ({vmsWithSnaps.length})
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
      to={`/snapshots/${encodeURIComponent(vm.uuid)}`}
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
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const nav = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const canSnap = hasPerm('vm.snapshot') && !features.data?.disable_snapshots;

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
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 2 }}>
            {vm.host_id} · <span className={`state-pill ${vm.state}`}>{vm.state}</span>
          </div>
        </div>
        <div style={{ display: 'flex', gap: 6 }}>
          <button
            type="button"
            className="btn-primary-sm"
            onClick={() => setCreateOpen(true)}
            disabled={!canSnap}
            title={!canSnap ? 'Requires vm.snapshot or feature disabled' : 'Create a new snapshot of this VM'}
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
                    <span style={currentPillStyle}>Current</span>
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
                      className="btn-primary-sm"
                      disabled={s.is_current || !canSnap}
                      onClick={() => revert.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name })}
                      title={!canSnap ? 'Requires vm.snapshot' : s.is_current ? 'Already current' : ''}
                    >
                      ↶ Revert
                    </button>
                    <button
                      className="btn-danger-sm"
                      disabled={!canSnap}
                      onClick={() => {
                        if (confirm(`Delete snapshot "${s.name}" from ${vm.name}?`)) {
                          del.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name });
                        }
                      }}
                    >
                      🗑 Delete
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
const currentPillStyle: React.CSSProperties = {
  marginLeft: 8,
  fontSize: 9,
  padding: '1px 6px',
  borderRadius: 10,
  background: '#3D2510',
  color: '#FF8A3C',
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
};
