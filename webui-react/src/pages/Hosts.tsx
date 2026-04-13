import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';

import { useHosts, useVMs } from '@/hooks/useInventory';
import type { Host } from '@/types';

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

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = hosts.data ?? [];
    if (!q) return list;
    return list.filter((h) => h.id.toLowerCase().includes(q) || h.ip.toLowerCase().includes(q));
  }, [hosts.data, query]);

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
          <table className="data-table">
            <thead>
              <tr>
                <th>Host</th>
                <th>IP</th>
                <th>Status</th>
                <th>VMs</th>
                <th>CPU</th>
                <th>Memory</th>
                <th>OS</th>
                <th>Uptime</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((h) => (
                <HostRow key={h.id} host={h} vmStats={vmCountByHost[h.id]} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}

function HostRow({ host, vmStats }: { host: Host; vmStats?: { total: number; running: number } }) {
  const memUsedGB = ((host.mem_total_mb - host.mem_free_mb) / 1024).toFixed(1);
  const memTotalGB = (host.mem_total_mb / 1024).toFixed(0);
  const cpuTone = host.cpu_usage > 85 ? '#F85149' : host.cpu_usage > 65 ? '#FFB020' : 'var(--text-body)';
  return (
    <tr>
      <td>
        <Link to={`/hosts/${encodeURIComponent(host.id)}`} style={nameLink}>
          {host.id}
        </Link>
      </td>
      <td style={{ color: 'var(--text-muted)', fontFamily: "'SF Mono', monospace", fontSize: 11 }}>{host.ip}</td>
      <td><span className={`state-pill ${host.status === 'online' ? 'running' : 'stopped'}`}>{host.status}</span></td>
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
