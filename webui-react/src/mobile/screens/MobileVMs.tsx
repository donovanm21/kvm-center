import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import type { VM } from '@/types';

import { VMStatePill } from './MobileHostDetail';

export function MobileVMs() {
  const vms = useQuery({ queryKey: qk.vms(), queryFn: () => apiGet<VM[]>('/api/vms') });
  const [filter, setFilter] = useState('');
  const [stateFilter, setStateFilter] = useState<'all' | 'running' | 'stopped'>('all');

  const list = (vms.data ?? [])
    // v5.46.2: stable sort first (by name) so polling refresh
    // doesn't shuffle the visible cards.
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name))
    .filter((v) => {
      if (stateFilter === 'running' && v.state !== 'running') return false;
      if (stateFilter === 'stopped' && v.state === 'running') return false;
      return true;
    })
    .filter((v) => {
      if (!filter) return true;
      const q = filter.toLowerCase();
      return v.name.toLowerCase().includes(q)
        || v.host_id.toLowerCase().includes(q)
        || (v.guest_hostname || '').toLowerCase().includes(q)
        || (v.tags ?? []).some(tag => tag.includes(q));
    });

  return (
    <div className="m-screen" data-testid="mobile-vms">
      <div className="m-screen-h">
        <h1 className="m-h1">VMs</h1>
        <p className="m-sub">{vms.data ? `${vms.data.length} virtual machines` : 'Loading…'}</p>
      </div>

      <input
        className="m-search"
        placeholder="Filter by name, host, hostname"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        autoCapitalize="none"
        autoCorrect="off"
      />

      <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
        {(['all', 'running', 'stopped'] as const).map((s) => (
          <button
            key={s}
            type="button"
            onClick={() => setStateFilter(s)}
            className={`m-pill ${stateFilter === s ? 'ok' : 'muted'}`}
            style={{ border: 'none', cursor: 'pointer', padding: '6px 12px', fontSize: 11 }}
          >{s}</button>
        ))}
      </div>

      {vms.isLoading && <div className="m-placeholder">Loading VMs…</div>}
      {vms.error && <div className="m-error">Failed to load VMs.</div>}
      {!vms.isLoading && list.length === 0 && (
        <div className="m-placeholder">No VMs match.</div>
      )}

      <div className="m-section">
        {list.map((v) => (
          <Link key={v.uuid} to={`/m/vms/${encodeURIComponent(v.uuid)}`} className="m-card-link">
            <div className="m-card">
              <div className="m-card-row">
                <div style={{ minWidth: 0 }}>
                  <div className="m-card-title">{v.name}</div>
                  <div className="m-card-meta">
                    {v.host_id} · {v.cpu_count} vCPU · {(v.mem_total_mb / 1024).toFixed(1)} GiB
                  </div>
                </div>
                <VMStatePill state={v.state} />
              </div>
            </div>
          </Link>
        ))}
      </div>
    </div>
  );
}
