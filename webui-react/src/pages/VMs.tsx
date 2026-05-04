import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { CreateVMModal } from '@/components/CreateVMModal';
import { KVMToolsPill } from '@/components/KVMToolsPill';
import { PaginationBar, usePagination } from '@/components/Pagination';
import { SortableTH, useSort } from '@/components/SortableTH';
import { LockBadge, StatePill, type LockReason } from '@/components/StatePill';
import { useFeatures, useKVMToolsLatest } from '@/hooks/useInventory';
import { useAuth } from '@/hooks/useAuth';
import { lockTitleFor, useDedupedVMs, useVMLocks } from '@/hooks/useVMLocks';
import type { VM } from '@/types';

// Re-export so existing consumers (HostDetail) keep their import shape.
// `lockKey` and `PHASES_PAST_DEFINE` moved to hooks/useVMLocks.ts so
// every VM view inherits the same lock semantics; both are still
// re-exported here for any caller still importing from this path.
export { lockKey, PHASES_PAST_DEFINE } from '@/hooks/useVMLocks';
export { LockBadge, type LockReason };

type VMSortKey = 'name' | 'host_id' | 'state' | 'cpu_count' | 'mem_total_mb' | 'disk_total_gb' | 'guest_agent';

export function VMs() {
  const vms = useDedupedVMs();
  const features = useFeatures();
  const { lockOf } = useVMLocks();
  const latest = useKVMToolsLatest();
  const { hasPerm } = useAuth();
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const { sort, onToggle, compare } = useSort<VMSortKey>('name', 'asc', 'vms');

  const canCreate = hasPerm('vm.create') && !features.data?.disable_vm_create;

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = vms.data ?? [];
    const base = !q
      ? list
      : list.filter((v) => v.name.toLowerCase().includes(q) || v.host_id.toLowerCase().includes(q));
    return [...base].sort((a, b) => compare(a[sort.key], b[sort.key]));
  }, [vms.data, query, sort, compare]);

  const pg = usePagination(filtered);

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
          <>
          <div className="data-table-scroll">
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
              {pg.items.map((v) => (
                <VMRow
                  key={v.uuid}
                  vm={v}
                  lock={lockOf(v)}
                  latestKVMTools={latest.data?.version ?? ''}
                />
              ))}
            </tbody>
          </table>
          </div>
          <PaginationBar pagination={pg} noun="VMs" />
          </>
        )}
      </div>
      <CreateVMModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </>
  );
}

function VMRow({ vm, lock, latestKVMTools }: { vm: VM; lock?: LockReason; latestKVMTools: string }) {
  const navigate = useNavigate();
  const locked = !!lock;
  const lockTitle = lockTitleFor(lock);
  const rowStyle: React.CSSProperties = locked
    ? { opacity: 0.45, cursor: 'not-allowed', background: 'var(--surface-2)' }
    : {};
  const onRowClick = locked
    ? undefined
    : () => navigate(`/vms/${encodeURIComponent(vm.uuid)}`);
  return (
    <tr
      className={locked ? '' : 'clickable'}
      style={rowStyle}
      title={lockTitle}
      onClick={onRowClick}
    >
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
      <td><StatePill state={vm.state} /></td>
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
      <td>
        <KVMToolsPill vm={vm} latest={latestKVMTools} showVersion />
      </td>
    </tr>
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
