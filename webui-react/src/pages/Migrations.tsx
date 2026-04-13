import { useMemo } from 'react';

import { useMigrations } from '@/hooks/useInventory';
import type { MigrationRecord } from '@/types';

export function Migrations() {
  const migrations = useMigrations();

  const { active, past } = useMemo(() => {
    const list = migrations.data ?? [];
    return {
      active: list.filter((m) => m.status === 'running'),
      past: list.filter((m) => m.status !== 'running'),
    };
  }, [migrations.data]);

  return (
    <>
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

      <SectionHeader label={`Migration history (${past.length})`} style={{ marginTop: active.length > 0 ? 18 : 0 }} />
      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {past.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: 'var(--text-muted)' }}>
            No migration history yet.
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
              {past.map((m) => (
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
    </>
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
