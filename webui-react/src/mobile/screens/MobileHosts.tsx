import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import type { Host } from '@/types';

export function MobileHosts() {
  const hosts = useQuery({ queryKey: qk.hosts(), queryFn: () => apiGet<Host[]>('/api/hosts') });
  const [filter, setFilter] = useState('');
  const list = (hosts.data ?? [])
    // v5.46.2: stable sort first so the list doesn't reorder on
    // every 10s React Query poll / SSE invalidation.
    .slice()
    .sort((a, b) => a.id.localeCompare(b.id))
    .filter((h) => {
      if (!filter) return true;
      const q = filter.toLowerCase();
      return h.id.toLowerCase().includes(q) || h.ip.toLowerCase().includes(q);
    });

  return (
    <div className="m-screen" data-testid="mobile-hosts">
      <div className="m-screen-h">
        <h1 className="m-h1">Hosts</h1>
        <p className="m-sub">{hosts.data ? `${hosts.data.length} in cluster` : 'Loading…'}</p>
      </div>

      <input
        className="m-search"
        placeholder="Filter by id or IP"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        autoCapitalize="none"
        autoCorrect="off"
      />

      {hosts.isLoading && <div className="m-placeholder">Loading hosts…</div>}
      {hosts.error && <div className="m-error">Failed to load hosts.</div>}
      {!hosts.isLoading && list.length === 0 && (
        <div className="m-placeholder">No hosts match.</div>
      )}

      {list.map((h) => {
        const memUsedPct = h.mem_total_mb > 0
          ? Math.round(((h.mem_total_mb - h.mem_free_mb) / h.mem_total_mb) * 100)
          : 0;
        const cpuPct = Math.round(h.cpu_usage || 0);
        return (
          <Link key={h.id} to={`/m/hosts/${encodeURIComponent(h.id)}`} className="m-card-link">
            <div className="m-card">
              <div className="m-card-row">
                <div style={{ minWidth: 0 }}>
                  <div className="m-card-title">{h.id}</div>
                  <div className="m-card-meta">{h.ip} · {h.vm_count} VMs</div>
                </div>
                <span className={`m-pill ${h.status === 'online' ? 'ok' : 'danger'}`}>
                  {h.status}
                </span>
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginTop: 12 }}>
                <Meter label={`CPU ${cpuPct}%`} pct={cpuPct} />
                <Meter label={`Mem ${memUsedPct}%`} pct={memUsedPct} />
              </div>
            </div>
          </Link>
        );
      })}
    </div>
  );
}

function Meter({ label, pct }: { label: string; pct: number }) {
  const tone = pct >= 90 ? 'danger' : pct >= 70 ? 'warn' : '';
  return (
    <div>
      <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
      <div className="m-meter"><div className={`m-meter-fill ${tone}`} style={{ width: `${Math.min(100, pct)}%` }} /></div>
    </div>
  );
}
