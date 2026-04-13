import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';

import { CreateVMModal } from '@/components/CreateVMModal';
import { SortableTH, useSort } from '@/components/SortableTH';
import { useVMs, useFeatures, useMigrations } from '@/hooks/useInventory';
import { useAuth } from '@/hooks/useAuth';
import type { VM } from '@/types';

type LockReason = 'migrating' | 'rolled-back';

// Phases that mean the destination already had the VM defined when the
// migration failed. The agent's rollback path tries to undefine + delete
// transferred files, but partial state can survive a failed rollback —
// keep the destination row gated until an operator clears it.
const PHASES_PAST_DEFINE = new Set(['define', 'start', 'verify', 'cleanup']);

function lockKey(hostID: string, vmName: string): string {
  return `${hostID}|${vmName}`;
}

type VMSortKey = 'name' | 'host_id' | 'state' | 'cpu_count' | 'mem_total_mb' | 'disk_total_gb' | 'guest_agent';

export function VMs() {
  const vms = useVMs();
  const features = useFeatures();
  const migrations = useMigrations();
  const { hasPerm } = useAuth();
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const { sort, onToggle, compare } = useSort<VMSortKey>('name');

  // Build a host|vm → lock-reason map from the migrations list. While a
  // migration is running we lock the source row to prevent a duplicate
  // start/stop/migrate against the VM that's already in flight. If the
  // migration fails after defining the VM on the destination, lock the
  // destination row instead — partial artifacts may remain there.
  const locks = useMemo(() => {
    const m = new Map<string, LockReason>();
    for (const r of migrations.data ?? []) {
      if (r.status === 'running' && r.source_host && r.vm_name) {
        m.set(lockKey(r.source_host, r.vm_name), 'migrating');
      } else if (r.status === 'failed' && r.dest_host && r.vm_name && PHASES_PAST_DEFINE.has(r.phase ?? '')) {
        m.set(lockKey(r.dest_host, r.vm_name), 'rolled-back');
      }
    }
    return m;
  }, [migrations.data]);

  const canCreate = hasPerm('vm.create') && !features.data?.disable_vm_create;

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = vms.data ?? [];
    const base = !q
      ? list
      : list.filter((v) => v.name.toLowerCase().includes(q) || v.host_id.toLowerCase().includes(q));
    return [...base].sort((a, b) => compare(a[sort.key], b[sort.key]));
  }, [vms.data, query, sort, compare]);

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
        <input
          type="search"
          placeholder="Search VMs or hosts…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={searchStyle}
        />
        <div style={{ flex: 1 }} />
        {canCreate ? (
          <button type="button" className="btn-primary-sm" onClick={() => setCreateOpen(true)}>
            + Create VM
          </button>
        ) : null}
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {vms.isLoading && <EmptyRow text="Loading VMs…" />}
        {vms.error && <EmptyRow text={`Failed to load VMs: ${(vms.error as Error).message}`} danger />}
        {vms.data && filtered.length === 0 && !vms.isLoading && (
          <EmptyRow text={query ? 'No VMs match your search.' : 'No VMs have reported yet.'} />
        )}
        {filtered.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <SortableTH colKey="name"           label="Name"         sort={sort} onToggle={onToggle} />
                <SortableTH colKey="host_id"        label="Host"         sort={sort} onToggle={onToggle} />
                <SortableTH colKey="state"          label="State"        sort={sort} onToggle={onToggle} />
                <SortableTH colKey="cpu_count"      label="CPUs"         sort={sort} onToggle={onToggle} />
                <SortableTH colKey="mem_total_mb"   label="Memory"       sort={sort} onToggle={onToggle} />
                <SortableTH colKey="disk_total_gb"  label="Disk"         sort={sort} onToggle={onToggle} />
                <SortableTH colKey="guest_agent"    label="Guest tools"  sort={sort} onToggle={onToggle} />
              </tr>
            </thead>
            <tbody>
              {filtered.map((v) => (
                <VMRow key={v.uuid} vm={v} lock={locks.get(lockKey(v.host_id, v.name))} />
              ))}
            </tbody>
          </table>
        )}
      </div>
      <CreateVMModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </>
  );
}

function VMRow({ vm, lock }: { vm: VM; lock?: LockReason }) {
  const locked = !!lock;
  const lockTitle = lock === 'migrating'
    ? 'Migration in progress — actions are disabled until it finishes'
    : lock === 'rolled-back'
      ? 'Migration rolled back — destination may have partial artifacts. Investigate before acting.'
      : '';
  const rowStyle: React.CSSProperties = locked
    ? { opacity: 0.45, cursor: 'not-allowed', background: 'var(--surface-2)' }
    : {};
  return (
    <tr className={locked ? '' : 'clickable'} style={rowStyle} title={lockTitle}>
      <td>
        {locked ? (
          <span style={{ ...nameLink, color: 'var(--text-muted)' }}>{vm.name}</span>
        ) : (
          <Link to={`/vms/${encodeURIComponent(vm.uuid)}`} style={nameLink}>
            {vm.name}
          </Link>
        )}
        {lock && <LockBadge reason={lock} />}
      </td>
      <td style={{ color: 'var(--text-muted)' }}>{vm.host_id}</td>
      <td><span className={`state-pill ${vm.state}`}>{vm.state}</span></td>
      <td>{vm.cpu_count}</td>
      <td>
        {vm.mem_total_mb > 0
          ? `${(vm.mem_usage_mb / 1024).toFixed(1)} / ${(vm.mem_total_mb / 1024).toFixed(0)} GB`
          : '—'}
      </td>
      <td>
        {vm.disk_total_gb > 0
          ? `${vm.disk_usage_gb.toFixed(1)} / ${vm.disk_total_gb.toFixed(0)} GB`
          : '—'}
      </td>
      <td style={{ color: vm.guest_agent ? '#3FB950' : 'var(--text-faded)' }}>
        {vm.guest_agent ? (vm.guest_source ?? 'present') : '—'}
      </td>
    </tr>
  );
}

function LockBadge({ reason }: { reason: LockReason }) {
  const isRollback = reason === 'rolled-back';
  return (
    <span
      style={{
        marginLeft: 8,
        display: 'inline-block',
        padding: '1px 8px',
        borderRadius: 8,
        fontSize: 9,
        fontWeight: 700,
        textTransform: 'uppercase',
        letterSpacing: '0.5px',
        background: isRollback ? '#3D1E1E' : '#3D2510',
        color: isRollback ? '#F85149' : '#FF8A3C',
        border: '1px solid ' + (isRollback ? '#5C2A2A' : '#5C3814'),
      }}
    >
      {isRollback ? 'rolled back' : 'migrating'}
    </span>
  );
}

function EmptyRow({ text, danger = false }: { text: string; danger?: boolean }) {
  return (
    <div style={{ padding: 20, textAlign: 'center', color: danger ? '#F85149' : 'var(--text-muted)', fontSize: 12 }}>
      {text}
    </div>
  );
}

const nameLink: React.CSSProperties = { color: 'var(--text-strong)', fontWeight: 500 };
const searchStyle: React.CSSProperties = {
  width: 320,
  height: 32,
  padding: '0 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 5,
  color: 'var(--text-body)',
  fontSize: 12,
};
