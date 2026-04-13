import { useMemo, useState } from 'react';

import { Pagination } from '@/components/Pagination';
import { useMigrations } from '@/hooks/useInventory';
import type { MigrationRecord } from '@/types';

type StatusFilter = 'all' | 'complete' | 'failed';
type ModeFilter = 'all' | 'live' | 'offline';

const HISTORY_PER_PAGE = 15;

export function Migrations() {
  const migrations = useMigrations();
  const [query, setQuery] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [modeFilter, setModeFilter] = useState<ModeFilter>('all');
  const [page, setPage] = useState(1);

  const { active, past, successful, failed } = useMemo(() => {
    const list = migrations.data ?? [];
    return {
      active: list.filter((m) => m.status === 'running'),
      past: list.filter((m) => m.status !== 'running'),
      successful: list.filter((m) => m.status === 'complete').length,
      failed: list.filter((m) => m.status === 'failed').length,
    };
  }, [migrations.data]);

  // Filter applies to the history list only; active migrations are
  // always shown so an operator never loses sight of them mid-search.
  const filteredPast = useMemo(() => {
    const q = query.trim().toLowerCase();
    return past.filter((m) => {
      if (statusFilter !== 'all' && m.status !== statusFilter) return false;
      if (modeFilter !== 'all' && m.mode !== modeFilter) return false;
      if (!q) return true;
      return (
        m.vm_name.toLowerCase().includes(q) ||
        m.source_host.toLowerCase().includes(q) ||
        m.dest_host.toLowerCase().includes(q)
      );
    });
  }, [past, query, statusFilter, modeFilter]);

  const totalPages = Math.max(1, Math.ceil(filteredPast.length / HISTORY_PER_PAGE));
  const clampedPage = Math.min(page, totalPages);
  const pagedPast = filteredPast.slice(
    (clampedPage - 1) * HISTORY_PER_PAGE,
    clampedPage * HISTORY_PER_PAGE,
  );

  const total = (migrations.data ?? []).length;

  return (
    <>
      {/* Summary row: total / successful / failed — mirrors v3. Each card
          carries a tone cue on the left edge so the state is legible
          from across the room. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: 12, marginBottom: 14 }}>
        <StatCard
          label="Total migrations"
          value={total}
          sub={`${active.length} active`}
          accent="#E3571E"
        />
        <StatCard
          label="Successful"
          value={successful}
          accent="#3FB950"
        />
        <StatCard
          label="Failed"
          value={failed}
          accent={failed > 0 ? '#F85149' : '#3FB950'}
        />
      </div>

      {migrations.isLoading && (
        <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading migration history…</div>
      )}
      {migrations.error && (
        <div className="panel" style={{ color: '#F85149', fontSize: 12 }}>
          Failed to load: {(migrations.error as Error).message}
        </div>
      )}

      {active.length > 0 && (
        <>
          <SectionHeader label={`Active migrations (${active.length})`} />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {active.map((m) => (
              <ActiveCard key={m.id} m={m} />
            ))}
          </div>
        </>
      )}

      <SectionHeader
        label={`Migration history (${filteredPast.length}${filteredPast.length !== past.length ? ` of ${past.length}` : ''})`}
        style={{ marginTop: active.length > 0 ? 18 : 0 }}
      />

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
        <input
          type="search"
          placeholder="Filter by VM, source host, or destination host…"
          value={query}
          onChange={(e) => { setQuery(e.target.value); setPage(1); }}
          style={searchStyle}
        />
        <FilterSelect
          label="Status"
          value={statusFilter}
          onChange={(v) => { setStatusFilter(v as StatusFilter); setPage(1); }}
          options={[
            { value: 'all', label: 'All' },
            { value: 'complete', label: 'Successful' },
            { value: 'failed', label: 'Failed' },
          ]}
        />
        <FilterSelect
          label="Mode"
          value={modeFilter}
          onChange={(v) => { setModeFilter(v as ModeFilter); setPage(1); }}
          options={[
            { value: 'all', label: 'All' },
            { value: 'live', label: 'Live' },
            { value: 'offline', label: 'Offline' },
          ]}
        />
        {(query || statusFilter !== 'all' || modeFilter !== 'all') && (
          <button
            type="button"
            className="btn-secondary-sm"
            onClick={() => { setQuery(''); setStatusFilter('all'); setModeFilter('all'); setPage(1); }}
          >
            Clear
          </button>
        )}
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {filteredPast.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: 'var(--text-muted)' }}>
            {past.length === 0
              ? 'No migration history yet.'
              : 'No migrations match the current filters.'}
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>VM</th>
                <th>Source</th>
                <th>Destination</th>
                <th>Mode</th>
                <th>Status</th>
                <th>Started</th>
                <th>Duration</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {pagedPast.map((m) => (
                <tr key={m.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{m.vm_name}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{m.source_host}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{m.dest_host}</td>
                  <td><ModePill mode={m.mode} /></td>
                  <td><StatusPill status={m.status} /></td>
                  <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{fmtTime(m.started_at)}</td>
                  <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{fmtDuration(m.elapsed_sec)}</td>
                  <td
                    style={{
                      color: m.status === 'failed' ? '#F85149' : 'var(--text-muted)',
                      fontSize: 11,
                      maxWidth: 360,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                    title={m.error || m.detail || ''}
                  >
                    {m.error || m.detail || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {totalPages > 1 && (
        <Pagination
          page={clampedPage}
          totalPages={totalPages}
          totalItems={filteredPast.length}
          perPage={HISTORY_PER_PAGE}
          onChange={setPage}
          noun="migrations"
        />
      )}
    </>
  );
}

function FilterSelect({
  label, value, onChange, options,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--text-faded)' }}>
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        style={{
          height: 32, padding: '0 28px 0 10px',
          background: 'var(--bg-input)',
          border: '1px solid var(--border)',
          borderRadius: 5,
          color: 'var(--text-body)',
          fontSize: 12,
          fontFamily: 'inherit',
        }}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </label>
  );
}

const searchStyle: React.CSSProperties = {
  flex: 1, minWidth: 240, height: 32, padding: '0 12px',
  background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 5,
  color: 'var(--text-body)', fontSize: 12,
};

function StatCard({ label, value, sub, accent }: { label: string; value: number; sub?: string; accent: string }) {
  return (
    <div
      className="panel"
      style={{ padding: '14px 18px', borderLeft: `3px solid ${accent}` }}
    >
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)' }}>
        {label}
      </div>
      <div style={{ fontSize: 30, fontWeight: 300, color: 'var(--text-strong)', marginTop: 6 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function ActiveCard({ m }: { m: MigrationRecord }) {
  const pct = m.progress ?? 0;
  return (
    <div className="panel" style={{ borderLeft: '3px solid var(--accent)' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 }}>
        <div>
          <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--text-strong)' }}>{m.vm_name}</div>
          <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>
            {m.source_host} → {m.dest_host} · {m.mode}
          </div>
        </div>
        <StatusPill status={m.status} />
      </div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
        <span style={{ fontSize: 12, color: 'var(--text-body)' }}>{m.phase || 'starting'}</span>
        <span style={{ fontSize: 12, color: 'var(--text-strong)' }}>{pct}%</span>
      </div>
      <div style={{ height: 8, background: 'var(--surface-3)', borderRadius: 4, overflow: 'hidden' }}>
        <div style={{ width: `${Math.min(pct, 100)}%`, height: '100%', background: '#E3571E', transition: 'width 0.5s' }} />
      </div>
      <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text-faded)' }}>
        {m.detail || ''} · Started {fmtTime(m.started_at)}
      </div>
    </div>
  );
}

function StatusPill({ status }: { status: string }) {
  const bg =
    status === 'running' ? '#3D2510' :
    status === 'complete' ? '#1E3D23' :
    status === 'failed' ? '#3D1E1E' :
    'var(--surface-3)';
  const color =
    status === 'running' ? '#FF8A3C' :
    status === 'complete' ? '#3FB950' :
    status === 'failed' ? '#F85149' :
    'var(--text-muted)';
  return <span style={{ ...pillStyle, background: bg, color }}>{status}</span>;
}

function ModePill({ mode }: { mode: string }) {
  const bg = mode === 'live' ? '#3D2510' : '#2D1810';
  const color = mode === 'live' ? '#ff9800' : '#FF8A3C';
  return <span style={{ ...pillStyle, background: bg, color }}>{mode}</span>;
}

function SectionHeader({ label, style }: { label: string; style?: React.CSSProperties }) {
  return (
    <div
      style={{
        fontSize: 10,
        textTransform: 'uppercase',
        letterSpacing: '0.08em',
        color: 'var(--text-faded)',
        margin: '0 0 8px 4px',
        ...style,
      }}
    >
      {label}
    </div>
  );
}

function fmtTime(ts?: number): string {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleDateString() + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function fmtDuration(sec?: number): string {
  if (!sec) return '—';
  if (sec < 60) return `${sec}s`;
  return `${Math.floor(sec / 60)}m ${sec % 60}s`;
}

const pillStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  padding: '2px 10px',
  borderRadius: 10,
  fontSize: 10,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
};
