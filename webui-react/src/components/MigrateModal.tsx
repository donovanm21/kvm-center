import { useEffect, useMemo, useState } from 'react';

import { Modal } from './Modal';
import { MigrationProgressCard } from './MigrationCenter';
import { useMigrations } from '@/hooks/useInventory';
import { useVMCommand } from '@/hooks/useVMCommand';
import type { Host, VM } from '@/types';

type Mode = 'offline' | 'live';

interface Check {
  ok: boolean;
  label: string;
  detail: string;
}

interface Props {
  vm: VM;
  hosts: Host[];
  open: boolean;
  onClose: () => void;
}

/**
 * Pre-flight migration dialog. Picks a target host from the online peers
 * (excluding the VM's current one) and runs the same set of compatibility
 * checks the v3 UI did — live migration has the stricter set because the
 * VM's CPU state is being streamed to the new host mid-flight.
 */
export function MigrateModal({ vm, hosts, open, onClose }: Props) {
  const onlinePeers = useMemo(
    () => hosts.filter((h) => h.id !== vm.host_id && h.status === 'online'),
    [hosts, vm.host_id],
  );
  const [mode, setMode] = useState<Mode>('offline');
  const [targetID, setTargetID] = useState<string>('');
  const [copyStorage, setCopyStorage] = useState(true);
  const [keepRaw, setKeepRaw] = useState(false);
  // Tracks whether we've already kicked off a migration in this modal
  // session — once true we swap the form for a live progress view.
  const [submittedAt, setSubmittedAt] = useState<number | null>(null);
  const cmd = useVMCommand();
  const migrations = useMigrations();

  // Reset transient state every time the modal is reopened.
  useEffect(() => {
    if (!open) setSubmittedAt(null);
  }, [open]);

  // Find the live record for the just-started migration: most recent
  // running record for this VM that started after we hit submit.
  const activeRecord = useMemo(() => {
    if (submittedAt === null) return undefined;
    return (migrations.data ?? [])
      .filter((m) => m.vm_name === vm.name && m.started_at * 1000 >= submittedAt - 5_000)
      .sort((a, b) => b.started_at - a.started_at)[0];
  }, [migrations.data, vm.name, submittedAt]);

  const effectiveTarget = targetID || onlinePeers[0]?.id;
  const sourceHost = hosts.find((h) => h.id === vm.host_id);
  const target = hosts.find((h) => h.id === effectiveTarget);

  const checks: Check[] = useMemo(() => {
    if (!target) return [];
    const memOk = (target.mem_free_mb || 0) >= (vm.mem_total_mb || 0);
    const memDetail = `${(target.mem_free_mb / 1024).toFixed(1)} GB free, VM needs ${(vm.mem_total_mb / 1024).toFixed(1)} GB`;
    if (mode === 'offline') {
      return [
        { ok: target.status === 'online', label: 'Target host is online', detail: target.status },
        { ok: memOk, label: 'Sufficient free memory', detail: memDetail },
        { ok: true, label: 'CPU compatibility', detail: 'Not required for offline migration' },
        { ok: true, label: 'PCI passthrough', detail: 'OK — VM will be shut down during migration' },
        { ok: true, label: 'Disk format', detail: 'qemu-img convert handles all formats including raw' },
      ];
    }
    // Live
    return [
      { ok: target.status === 'online', label: 'Target host is online', detail: target.status },
      { ok: memOk, label: 'Sufficient free memory', detail: memDetail },
      {
        ok: (sourceHost?.cpu_model || '') === (target.cpu_model || '') || vm.cpu_mode !== 'host-passthrough',
        label: 'CPU compatibility',
        detail: (sourceHost?.cpu_model || '') === (target.cpu_model || '')
          ? 'Same CPU model'
          : 'host-passthrough requires matching CPUs on both ends',
      },
      {
        ok: !(vm.pci_devices && vm.pci_devices.length > 0),
        label: 'No PCI passthrough devices',
        detail: vm.pci_devices?.length
          ? `${vm.pci_devices.length} device(s) block live migration`
          : 'None',
      },
      {
        ok: vm.disk_type !== 'raw',
        label: 'Disk format supports migration',
        detail: vm.disk_type ?? 'qcow2',
      },
      {
        ok: !(vm.snapshot_count > 0),
        label: 'No snapshots',
        detail: vm.snapshot_count > 0
          ? `${vm.snapshot_count} snapshot(s) — delete first`
          : 'None',
      },
    ];
  }, [mode, target, sourceHost, vm]);

  const allOk = checks.every((c) => c.ok);

  const submit = () => {
    if (!target || !allOk) return;
    const params: Record<string, string> = { destination: target.ip };
    if (mode === 'live') {
      params.copy_storage = copyStorage ? 'true' : 'false';
    } else {
      params.keep_raw = keepRaw ? 'true' : 'false';
    }
    setSubmittedAt(Date.now());
    cmd.mutate({
      host_id: vm.host_id,
      vm_uuid: vm.uuid,
      vm_name: vm.name,
      action: mode === 'live' ? 'MIGRATE' : 'OFFLINE_MIGRATE',
      params,
    });
  };

  if (onlinePeers.length === 0) {
    return (
      <Modal
        open={open}
        onClose={onClose}
        title={`Migrate — ${vm.name}`}
        footer={<button className="btn-secondary-sm" onClick={onClose}>Close</button>}
      >
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>
          No other online hypervisors are available — need at least one healthy peer to migrate to.
        </div>
      </Modal>
    );
  }

  if (submittedAt !== null) {
    // Post-submit progress view. The modal can be closed (the migration
    // keeps running on the agent + server) and reopened from the
    // Topbar's Migration Center badge.
    return (
      <Modal
        open={open}
        onClose={onClose}
        width={520}
        title={`Migrating — ${vm.name}`}
        footer={
          <button className="btn-secondary-sm" onClick={onClose}>
            Run in background
          </button>
        }
      >
        {activeRecord ? (
          <MigrationProgressCard m={activeRecord} />
        ) : (
          <div style={{ fontSize: 12, color: 'var(--text-muted)', padding: '8px 0' }}>
            Starting migration… progress will appear here once the agent reports back.
          </div>
        )}
        <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text-faded)' }}>
          You can close this modal — the migration will continue on the source agent.
          Reopen progress at any time from the Migration Center button in the top bar.
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      width={580}
      title={`Migrate — ${vm.name}`}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose} disabled={cmd.isPending}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={!allOk || cmd.isPending}>
            {cmd.isPending
              ? 'Migrating…'
              : mode === 'offline'
                ? 'Shut Down & Migrate'
                : 'Live Migrate'}
          </button>
        </>
      }
    >
      <div style={modeBarStyle}>
        <ModeTab label="Offline (Recommended)" active={mode === 'offline'} onClick={() => setMode('offline')} tone="accent" />
        <ModeTab label="Live" active={mode === 'live'} onClick={() => setMode('live')} tone="warn" />
      </div>

      <div style={modeDescStyle(mode)}>
        {mode === 'offline' ? (
          <>
            <strong style={{ color: 'var(--text-strong)' }}>Offline migration:</strong>{' '}
            Shuts down the VM, compresses the disk with <code>qemu-img convert</code>{' '}
            (dramatically smaller transfer), copies to the target, defines and starts it there.{' '}
            <strong style={{ color: '#4caf50' }}>Recommended for local storage.</strong>
          </>
        ) : (
          <>
            <strong style={{ color: 'var(--text-strong)' }}>Live migration:</strong>{' '}
            Copies the full disk image while the VM keeps running.{' '}
            <strong style={{ color: '#ff9800' }}>Very slow without shared storage</strong>{' '}
            — copies the entire virtual disk size, not just allocated blocks.
          </>
        )}
      </div>

      <Field label="Source host">
        <div style={{ fontSize: 13, color: 'var(--text-strong)', fontWeight: 600, padding: '4px 0' }}>
          {vm.host_id}
        </div>
      </Field>

      <Field label="Target host">
        <select
          value={effectiveTarget ?? ''}
          onChange={(e) => setTargetID(e.target.value)}
          style={selectStyle}
        >
          {onlinePeers.map((h) => (
            <option key={h.id} value={h.id}>
              {h.id} ({h.ip}) — {(h.mem_free_mb / 1024).toFixed(1)} GB free
            </option>
          ))}
        </select>
      </Field>

      {mode === 'live' && (
        <div style={{ marginTop: 12, padding: '10px 14px', background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4 }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text-strong)', marginBottom: 8 }}>
            Storage transfer
          </div>
          <label style={checkboxLabel}>
            <input type="checkbox" checked={copyStorage} onChange={(e) => setCopyStorage(e.target.checked)} />
            <span>
              <strong>Copy storage</strong>{' '}
              <span style={{ color: 'var(--text-faded)' }}>
                — transfers disk images too. Leave on unless source + target share storage.
              </span>
            </span>
          </label>
        </div>
      )}

      {mode === 'offline' && vm.disk_type === 'raw' && (
        <div style={{ marginTop: 12, padding: '10px 14px', background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4 }}>
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginBottom: 10 }}>
            This VM uses <strong style={{ color: '#ff9800' }}>raw</strong> disk images. The disk is always
            transferred as compressed qcow2 for speed. Choose the final format on the target:
          </div>
          <label style={{ ...checkboxLabel, marginBottom: 6 }}>
            <input type="radio" checked={!keepRaw} onChange={() => setKeepRaw(false)} />
            <span>
              <strong>Convert to qcow2</strong>
              <br />
              <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>
                Enables snapshots, thin provisioning, smaller footprint.
              </span>
            </span>
          </label>
          <label style={checkboxLabel}>
            <input type="radio" checked={keepRaw} onChange={() => setKeepRaw(true)} />
            <span>
              <strong>Convert back to raw on the target</strong>
              <br />
              <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>
                Match source format — larger on-disk footprint, no snapshot support.
              </span>
            </span>
          </label>
        </div>
      )}

      <div style={{ marginTop: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6 }}>
          Pre-flight checks
        </div>
        <div style={{ border: '1px solid var(--border)', borderRadius: 4, background: 'var(--bg-input)' }}>
          {checks.map((c, i) => (
            <div
              key={i}
              style={{
                display: 'flex',
                gap: 12,
                padding: '8px 12px',
                borderBottom: i === checks.length - 1 ? 'none' : '1px solid var(--border-subtle)',
                fontSize: 12,
              }}
            >
              <span style={{ color: c.ok ? '#3FB950' : '#F85149', fontWeight: 600, minWidth: 14 }}>
                {c.ok ? '✓' : '✕'}
              </span>
              <span style={{ flex: 1, color: c.ok ? 'var(--text-body)' : 'var(--text-strong)' }}>{c.label}</span>
              <span style={{ color: 'var(--text-faded)', fontSize: 11, textAlign: 'right' }}>{c.detail}</span>
            </div>
          ))}
        </div>
      </div>
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginTop: 12 }}>
      <div
        style={{
          fontSize: 11,
          color: 'var(--text-muted)',
          textTransform: 'uppercase',
          letterSpacing: '0.08em',
          marginBottom: 6,
        }}
      >
        {label}
      </div>
      {children}
    </div>
  );
}

function ModeTab({
  label,
  active,
  onClick,
  tone,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
  tone: 'accent' | 'warn';
}) {
  const color = tone === 'accent' ? '#E3571E' : '#ff9800';
  return (
    <div
      onClick={onClick}
      role="tab"
      aria-selected={active}
      style={{
        cursor: 'pointer',
        padding: '6px 14px',
        fontSize: 12,
        borderBottom: `2px solid ${active ? color : 'transparent'}`,
        color: active ? 'var(--text-strong)' : 'var(--text-muted)',
      }}
    >
      {label}
    </div>
  );
}

const modeBarStyle: React.CSSProperties = {
  display: 'flex',
  gap: 4,
  marginBottom: 10,
  borderBottom: '1px solid var(--border)',
};

const modeDescStyle = (mode: Mode): React.CSSProperties => ({
  fontSize: 11,
  color: 'var(--text-faded)',
  margin: '8px 0',
  padding: '10px 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderLeft: `3px solid ${mode === 'offline' ? '#E3571E' : '#ff9800'}`,
  borderRadius: 4,
  lineHeight: 1.5,
});

const selectStyle: React.CSSProperties = {
  width: '100%',
  height: 34,
  padding: '0 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
};

const checkboxLabel: React.CSSProperties = {
  display: 'flex',
  alignItems: 'flex-start',
  gap: 8,
  cursor: 'pointer',
  fontSize: 12,
  color: 'var(--text-body)',
  lineHeight: 1.5,
};
