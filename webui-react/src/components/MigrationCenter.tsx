import { useMutation, useQueryClient } from '@tanstack/react-query';

import { Modal } from './Modal';
import { apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useMigrations } from '@/hooks/useInventory';
import { useToast } from '@/hooks/useToast';
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
 * MigrateModal's post-submit view. Phase + progress come from the
 * store via useMigrations; Cancel hits the new server endpoint which
 * proxies to the agent's /cancel-migrate.
 */
export function MigrationProgressCard({ m }: { m: MigrationRecord }) {
  const pct = Math.min(m.progress ?? 0, 100);
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => apiPost(`/api/migrations/cancel?id=${encodeURIComponent(m.id)}`),
    onSuccess: () => {
      toast('success', `Cancelling migration of ${m.vm_name}`);
      qc.invalidateQueries({ queryKey: ['migrations'] });
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast('error', `Cancel failed: ${msg}`);
    },
  });

  // Cancellation is unsafe past the cleanup phase — source disks are
  // gone by then. Also disabled while a cancel request is already in
  // flight or once the record reports "cancelling".
  const phase = m.phase ?? '';
  const tooLate = phase === 'cleanup';
  const alreadyCancelling = phase === 'cancelling';
  const canCancel = hasPerm('vm.migrate') && m.status === 'running' && !tooLate && !alreadyCancelling && !cancel.isPending;
  const cancelTitle = !hasPerm('vm.migrate')
    ? 'Requires vm.migrate'
    : tooLate
      ? 'Source disks have already been removed during the cleanup phase — too late to cancel'
      : alreadyCancelling
        ? 'Cancellation already in progress'
        : `Cancel migration and roll back to ${m.source_host}`;

  const onCancelClick = () => {
    if (!canCancel) return;
    if (confirm(`Cancel migration of "${m.vm_name}" and restore it on ${m.source_host}?`)) {
      cancel.mutate();
    }
  };

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
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <StatusPill status={alreadyCancelling ? 'cancelling' : m.status} />
          <button
            type="button"
            className="btn-danger-sm"
            disabled={!canCancel}
            title={cancelTitle}
            onClick={onCancelClick}
            style={{ height: 24, padding: '0 10px', fontSize: 11 }}
          >
            {cancel.isPending || alreadyCancelling ? 'Cancelling…' : 'Cancel'}
          </button>
        </div>
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
    status === 'cancelling' ? '#3D1E1E' :
    status === 'complete' ? '#1E3D23' :
    status === 'failed' ? '#3D1E1E' :
    'var(--surface-3)';
  const color =
    status === 'running' ? '#FF8A3C' :
    status === 'cancelling' ? '#F85149' :
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
