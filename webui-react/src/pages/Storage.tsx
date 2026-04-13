import { useMemo, useState } from 'react';

import { FileBrowserModal } from '@/components/FileBrowserModal';
import { SortableTH, useSort } from '@/components/SortableTH';
import { useHosts, useStorage } from '@/hooks/useInventory';
import type { StoragePool } from '@/types';

type PoolSortKey = 'name' | 'host_id' | 'pool_type' | 'state' | 'capacity_gb' | 'allocation_gb' | 'available_gb' | 'path';

interface BrowseTarget {
  hostID: string;
  path: string;
}

export function Storage() {
  const storage = useStorage();
  const hosts = useHosts();
  const [query, setQuery] = useState('');
  const [browse, setBrowse] = useState<BrowseTarget | null>(null);

  const { sort, onToggle, compare } = useSort<PoolSortKey>('name');

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = storage.data ?? [];
    const base = !q
      ? list
      : list.filter(
          (p) => p.name.toLowerCase().includes(q) || p.host_id.toLowerCase().includes(q),
        );
    return [...base].sort((a, b) => compare(a[sort.key], b[sort.key]));
  }, [storage.data, query, sort, compare]);

  return (
    <>
      <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14 }}>
        <input
          type="search"
          placeholder="Search storage pools…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={searchStyle}
        />
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {storage.isLoading && <EmptyCell text="Loading pools…" />}
        {storage.error && <EmptyCell text={`Failed: ${(storage.error as Error).message}`} danger />}
        {storage.data && filtered.length === 0 && !storage.isLoading && (
          <EmptyCell text={query ? 'No pools match your search.' : 'No storage pools reported yet.'} />
        )}
        {filtered.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <SortableTH colKey="name"          label="Pool"      sort={sort} onToggle={onToggle} />
                <SortableTH colKey="host_id"       label="Host"      sort={sort} onToggle={onToggle} />
                <SortableTH colKey="pool_type"     label="Type"      sort={sort} onToggle={onToggle} />
                <SortableTH colKey="state"         label="State"     sort={sort} onToggle={onToggle} />
                <SortableTH colKey="capacity_gb"   label="Capacity"  sort={sort} onToggle={onToggle} />
                <SortableTH colKey="allocation_gb" label="Used"      sort={sort} onToggle={onToggle} />
                <SortableTH colKey="available_gb"  label="Available" sort={sort} onToggle={onToggle} />
                <th style={{ width: 220 }}>Utilisation</th>
                <SortableTH colKey="path"          label="Path"      sort={sort} onToggle={onToggle} />
                <th style={{ width: 80 }} />
              </tr>
            </thead>
            <tbody>
              {filtered.map((p) => (
                <PoolRow
                  key={`${p.host_id}:${p.name}`}
                  pool={p}
                  onBrowse={() => p.path && setBrowse({ hostID: p.host_id, path: p.path })}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>

      {browse && (
        <FileBrowserModal
          hostID={browse.hostID}
          initialPath={browse.path}
          open
          onClose={() => setBrowse(null)}
        />
      )}
      {/* hosts kept in cache for the file browser upload breadcrumb */}
      <span hidden>{hosts.isSuccess ? '' : ''}</span>
    </>
  );
}

function PoolRow({ pool, onBrowse }: { pool: StoragePool; onBrowse: () => void }) {
  const pct = pool.capacity_gb > 0 ? (pool.allocation_gb / pool.capacity_gb) * 100 : 0;
  const tone = pct > 85 ? 'danger' : pct > 65 ? 'warn' : 'ok';
  const color = tone === 'danger' ? '#F85149' : tone === 'warn' ? '#FFB020' : '#3FB950';
  return (
    <tr>
      <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{pool.name}</td>
      <td style={{ color: 'var(--text-muted)' }}>{pool.host_id}</td>
      <td style={{ color: 'var(--text-muted)' }}>{pool.pool_type || '—'}</td>
      <td>
        <span className={`state-pill ${pool.state === 'running' ? 'running' : 'stopped'}`}>
          {pool.state}
        </span>
      </td>
      <td>{pool.capacity_gb.toFixed(1)} GB</td>
      <td>{pool.allocation_gb.toFixed(1)} GB</td>
      <td>{pool.available_gb.toFixed(1)} GB</td>
      <td>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <div style={{ width: 120, height: 6, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
            <div style={{ width: `${Math.min(pct, 100).toFixed(1)}%`, height: '100%', background: color }} />
          </div>
          <span style={{ fontSize: 11, color: 'var(--text-muted)', minWidth: 40 }}>
            {pct.toFixed(0)}%
          </span>
        </div>
      </td>
      <td
        style={{
          fontFamily: "'SF Mono', monospace",
          fontSize: 11,
          color: 'var(--text-muted)',
          maxWidth: 260,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
        title={pool.path}
      >
        {pool.path || '—'}
      </td>
      <td>
        <button
          type="button"
          className="btn-secondary-sm"
          disabled={!pool.path}
          onClick={onBrowse}
        >
          Browse
        </button>
      </td>
    </tr>
  );
}

function EmptyCell({ text, danger = false }: { text: string; danger?: boolean }) {
  return (
    <div style={{ padding: 20, fontSize: 12, color: danger ? '#F85149' : 'var(--text-muted)', textAlign: 'center' }}>
      {text}
    </div>
  );
}

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
