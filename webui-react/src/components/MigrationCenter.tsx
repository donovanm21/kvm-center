import { Modal } from './Modal';
import { useMigrations } from '@/hooks/useInventory';
import type { MigrationRecord } from '@/types';

interface Props {
  open: boolean;
  onClose: () => void;
}

/**
 * Modal that lists every in-flight migration with its current phase,
 * progress, and detail line. Reachable from the Topbar's "active
 * migrations" badge so an operator who minimised MigrateModal mid-flight
 * can pop the progress view back open from anywhere in the app.
 */
export function MigrationCenter({ open, onClose }: Props) {
  const migrations = useMigrations();
  const active = (migrations.data ?? []).filter((m) => m.status === 'running');

  return (
    <Modal
      open={open}
      onClose={onClose}
      width={620}
      title={`Active migrations${active.length > 0 ? ` (${active.length})` : ''}`}
      footer={<button className="btn-secondary-sm" onClick={onClose}>Close</button>}
    >
      {active.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text-muted)', padding: '8px 0' }}>
          No migrations are currently in flight.
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {active.map((m) => (
            <MigrationProgressCard key={m.id} m={m} />
          ))}
        </div>
      )}
    </Modal>
  );
}

/**
 * Compact progress card used by both the Migration Center modal and
 * MigrateModal's post-submit view. Pure presentational component — the
 * phase + progress numbers come from the store via useMigrations.
 */
export function MigrationProgressCard({ m }: { m: MigrationRecord }) {
  const pct = Math.min(m.progress ?? 0, 100);
  return (
    <div
      style={{
        border: '1px solid var(--border)',
        borderLeft: '3px solid var(--accent)',
        borderRadius: 4,
        background: 'var(--bg-input)',
        padding: '12px 14px',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)' }}>{m.vm_name}</div>
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
      <div style={{ height: 6, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
        <div
          style={{
            width: `${pct}%`,
            height: '100%',
            background: '#E3571E',
            transition: 'width 0.5s',
          }}
        />
      </div>
      {m.detail && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text-faded)' }}>{m.detail}</div>
      )}
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
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center',
      padding: '2px 10px', borderRadius: 10,
      fontSize: 10, fontWeight: 600,
      textTransform: 'uppercase', letterSpacing: '0.5px',
      background: bg, color,
    }}>{status}</span>
  );
}
