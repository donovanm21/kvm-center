import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { PaginationBar, usePagination } from '@/components/Pagination';
import { SortableTH, useSort } from '@/components/SortableTH';
import { HostStatusPill } from '@/components/StatePill';
import { useHosts, useVMs } from '@/hooks/useInventory';
import type { Host } from '@/types';

type HostSortKey = 'id' | 'ip' | 'status' | 'vm_count' | 'cpu_usage' | 'mem_total_mb' | 'os' | 'uptime';

export function Hosts() {
  const hosts = useHosts();
  const vms = useVMs();
  const [query, setQuery] = useState('');

  const vmCountByHost = useMemo(() => {
    const out: Record<string, { total: number; running: number }> = {};
    for (const v of vms.data ?? []) {
      const entry = out[v.host_id] ?? { total: 0, running: 0 };
      entry.total += 1;
      if (v.state === 'running') entry.running += 1;
      out[v.host_id] = entry;
    }
    return out;
  }, [vms.data]);

  const { sort, onToggle, compare } = useSort<HostSortKey>('id', 'asc', 'hosts');

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = hosts.data ?? [];
    const base = !q
      ? list
      : list.filter((h) => {
          const hay = [
            h.id, h.ip, h.os ?? '', h.libvirt_ver ?? '', h.kernel_ver ?? '', h.status,
          ].join(' ').toLowerCase();
          return hay.includes(q);
        });
    return [...base].sort((a, b) => compare(a[sort.key], b[sort.key]));
  }, [hosts.data, query, sort, compare]);

  const pg = usePagination(filtered);

  return (
    <>
      <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14 }}>
        <input
          type="search"
          placeholder="Search hosts…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={searchStyle}
        />
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {hosts.isLoading && <Empty>Loading hosts…</Empty>}
        {hosts.error && <Empty danger>Failed: {(hosts.error as Error).message}</Empty>}
        {hosts.data && filtered.length === 0 && !hosts.isLoading && (
          <Empty>{query ? 'No hosts match your search.' : 'No hypervisors have reported yet.'}</Empty>
        )}
        {filtered.length > 0 && (
          <>
          <div className="data-table-scroll">
          <table className="data-table">
            <thead>
              <tr>
                <SortableTH colKey="id"            label="Host"   sort={sort} onToggle={onToggle} />
                <SortableTH colKey="ip"            label="IP"     sort={sort} onToggle={onToggle} />
                <SortableTH colKey="status"        label="Status" sort={sort} onToggle={onToggle} />
                <SortableTH colKey="vm_count"      label="VMs"    sort={sort} onToggle={onToggle} />
                <SortableTH colKey="cpu_usage"     label="CPU"    sort={sort} onToggle={onToggle} />
                <SortableTH colKey="mem_total_mb"  label="Memory" sort={sort} onToggle={onToggle} />
                <SortableTH colKey="os"            label="OS"     sort={sort} onToggle={onToggle} />
                <SortableTH colKey="uptime"        label="Uptime" sort={sort} onToggle={onToggle} />
              </tr>
            </thead>
            <tbody>
              {pg.items.map((h) => (
                <HostRow key={h.id} host={h} vmStats={vmCountByHost[h.id]} />
              ))}
            </tbody>
          </table>
          </div>
          <PaginationBar pagination={pg} noun="hosts" />
          </>
        )}
      </div>
    </>
  );
}

function HostRow({ host, vmStats }: { host: Host; vmStats?: { total: number; running: number } }) {
  const navigate = useNavigate();
  const memUsedGB = ((host.mem_total_mb - host.mem_free_mb) / 1024).toFixed(1);
  const memTotalGB = (host.mem_total_mb / 1024).toFixed(0);
  const cpuTone = host.cpu_usage > 85 ? '#F85149' : host.cpu_usage > 65 ? '#FFB020' : 'var(--text-body)';
  return (
    <tr
      className="clickable"
      onClick={() => navigate(`/hosts/${encodeURIComponent(host.id)}`)}
    >
      <td>
        <Link to={`/hosts/${encodeURIComponent(host.id)}`} style={nameLink}>
          {host.id}
        </Link>
      </td>
      <td style={{ color: 'var(--text-muted)', fontFamily: "'SF Mono', monospace", fontSize: 11 }}>{host.ip}</td>
      <td>
        <HostStatusPill status={host.status} />
        {host.maintenance_mode && <span className="tone-pill warn" style={{ marginLeft: 6 }} title={host.maintenance_reason || 'Maintenance mode'}>maintenance</span>}
      </td>
      <td>{vmStats ? `${vmStats.running} / ${vmStats.total}` : host.vm_count}</td>
      <td style={{ color: cpuTone }}>
        {host.cpu_usage.toFixed(1)}%<span style={{ color: 'var(--text-faded)', marginLeft: 4 }}>of {host.cpu_count}</span>
      </td>
      <td>{memUsedGB} / {memTotalGB} GB</td>
      <td style={{ color: 'var(--text-muted)' }}>{host.os || '—'}</td>
      <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{host.uptime || '—'}</td>
    </tr>
  );
}

function Empty({ children, danger = false }: { children: React.ReactNode; danger?: boolean }) {
  return (
    <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: danger ? '#F85149' : 'var(--text-muted)' }}>
      {children}
    </div>
  );
}

const nameLink: React.CSSProperties = { color: 'var(--text-strong)', fontWeight: 500 };
const searchStyle: React.CSSProperties = {
  width: 320, height: 32, padding: '0 12px',
  background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 5,
  color: 'var(--text-body)', fontSize: 12,
};
