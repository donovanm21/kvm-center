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
                      color: 'var(--text-muted)',
                      fontSize: 11,
                      maxWidth: 360,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                    title={m.error || m.detail || ''}
                  >
                    {m.status === 'failed' && m.error
                      ? <span className="tone-pill danger" style={{ fontSize: 10 }}>{summariseMigrationError(m.error)}</span>
                      : cleanMigrationDetail(m.detail) || '—'}
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
        <div style={{ width: `${Math.min(pct, 100)}%`, height: '100%', background: 'var(--accent)', transition: 'width 0.5s' }} />
      </div>
      <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text-faded)' }}>
        {cleanMigrationDetail(m.detail)} · Started {fmtTime(m.started_at)}
      </div>
    </div>
  );
}

function StatusPill({ status }: { status: string }) {
  const tone =
    status === 'running'  ? 'warn' :
    status === 'complete' ? 'ok' :
    status === 'failed'   ? 'danger' :
    'muted';
  return <span className={`tone-pill ${tone}`}>{status}</span>;
}

function ModePill({ mode }: { mode: string }) {
  // Both modes are "informational" but live is slightly hotter since it
  // carries more risk (stream-in-flight). Use warn for live, info for
  // offline so they're visually distinct without reading.
  return <span className={`tone-pill ${mode === 'live' ? 'warn' : 'info'}`}>{mode}</span>;
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

// summariseMigrationError maps a raw virsh / libvirt error string to a
// short phrase that fits the Detail column. Full text is still
// available via the cell's title attribute. Patterns match the errors
// we've actually seen in the field; unknown errors fall back to the
// first ~60 chars of the original.
function summariseMigrationError(err: string): string {
  const e = err.toLowerCase();
  if (e.includes('no route to host')) return 'no route to host (firewall?)';
  if (e.includes('connection refused')) return 'connection refused';
  if (e.includes('connection timed out')) return 'timed out';
  if (e.includes('authentication failed') || e.includes('certificate')) return 'TLS / cert auth failed';
  if (e.includes('unable to connect to server')) return 'cannot reach peer libvirtd';
  if (e.includes('already exists')) return 'target domain already exists';
  if (e.includes('cancelled by operator')) return 'cancelled';
  // Strip the "error: " prefix virsh prepends + collapse whitespace.
  const clean = err.replace(/^error:\s*/i, '').replace(/\s+/g, ' ').trim();
  return clean.length > 60 ? clean.slice(0, 57) + '…' : clean;
}

// cleanMigrationDetail trims the Migration: [XX.XX %] progress spam
// virsh emits on success into a single short status phrase.
function cleanMigrationDetail(detail?: string): string {
  if (!detail) return '';
  if (detail.startsWith('Migration: [')) return 'completed';
  return detail;
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

