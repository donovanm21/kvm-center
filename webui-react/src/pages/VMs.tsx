import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';

import { CreateVMModal } from '@/components/CreateVMModal';
import { useVMs, useFeatures } from '@/hooks/useInventory';
import { useAuth } from '@/hooks/useAuth';
import type { VM } from '@/types';

export function VMs() {
  const vms = useVMs();
  const features = useFeatures();
  const { hasPerm } = useAuth();
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);

  const canCreate = hasPerm('vm.create') && !features.data?.disable_vm_create;

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = vms.data ?? [];
    if (!q) return list;
    return list.filter(
      (v) => v.name.toLowerCase().includes(q) || v.host_id.toLowerCase().includes(q),
    );
  }, [vms.data, query]);

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
                <th>Name</th>
                <th>Host</th>
                <th>State</th>
                <th>CPUs</th>
                <th>Memory</th>
                <th>Disk</th>
                <th>Guest tools</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((v) => (
                <VMRow key={v.uuid} vm={v} />
              ))}
            </tbody>
          </table>
        )}
      </div>
      <CreateVMModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </>
  );
}

function VMRow({ vm }: { vm: VM }) {
  return (
    <tr className="clickable">
      <td>
        <Link to={`/vms/${encodeURIComponent(vm.uuid)}`} style={nameLink}>
          {vm.name}
        </Link>
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
