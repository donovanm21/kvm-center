import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';

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
  // Transport override (v5.11.4). "" lets the server auto-pick based
  // on host mTLS-readiness; operators who want to pin legacy ssh
  // during mTLS rollout can override here.
  const [transport, setTransport] = useState<'' | 'tls' | 'ssh'>('');
  // v5.11.7 advanced-modal options. Offline-only; all optional.
  const [targetPool, setTargetPool] = useState<string>('');
  const [newVMName, setNewVMName] = useState<string>('');
  const [regenerateMAC, setRegenerateMAC] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  // Tracks whether we've already kicked off a migration in this modal
  // session — once true we swap the form for a live progress view.
  const [submittedAt, setSubmittedAt] = useState<number | null>(null);
  const cmd = useVMCommand();
  const migrations = useMigrations();
  const navigate = useNavigate();

  // "Run in background" routes the user away from the in-flight VM so
  // they can't accidentally fire follow-up actions against it. Going to
  // the Migrations page also makes the live progress card visible
  // without an extra click on the Topbar badge.
  const sendToBackground = () => {
    onClose();
    navigate('/migrations');
  };

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

  // Pull the destination's pool list only when the advanced section
  // is open. Auto-selects "default" when the list loads so the
  // operator doesn't have to click when the fleet is uniform.
  const poolsQuery = useQuery({
    queryKey: ['host-pools', effectiveTarget],
    queryFn: () => apiGet<{ name: string; path: string; type: string; capacity_gb: number; available_gb: number }[]>(
      '/api/hosts/pools?host=' + encodeURIComponent(effectiveTarget || ''),
    ),
    enabled: advancedOpen && mode === 'offline' && !!effectiveTarget,
    staleTime: 15_000,
  });
  useEffect(() => {
    if (!targetPool && poolsQuery.data?.some((p) => p.name === 'default')) {
      setTargetPool('default');
    }
  }, [poolsQuery.data, targetPool]);

  const checks: Check[] = useMemo(() => {
    if (!target) return [];
    const memOk = (target.mem_free_mb || 0) >= (vm.mem_total_mb || 0);
    const memDetail = `${(target.mem_free_mb / 1024).toFixed(1)} GB free, VM needs ${(vm.mem_total_mb / 1024).toFixed(1)} GB`;
    const maintOk = !target.maintenance_mode;
    if (mode === 'offline') {
      return [
        { ok: target.status === 'online', label: 'Target host is online', detail: target.status },
        { ok: maintOk, label: 'Target host is NOT in maintenance', detail: maintOk ? 'available' : 'refusing placements (drain mode)' },
        { ok: memOk, label: 'Sufficient free memory', detail: memDetail },
        { ok: true, label: 'CPU compatibility', detail: 'Not required for offline migration' },
        { ok: true, label: 'PCI passthrough', detail: 'OK — VM will be shut down during migration' },
        { ok: true, label: 'Disk format', detail: 'qemu-img convert handles all formats including raw' },
      ];
    }
    // Live
    return [
      { ok: target.status === 'online', label: 'Target host is online', detail: target.status },
      { ok: maintOk, label: 'Target host is NOT in maintenance', detail: maintOk ? 'available' : 'refusing placements (drain mode)' },
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

  // Block submit if a migration is already in flight on either the source
  // or the selected target host. The server rejects this too (409), but
  // catching it client-side keeps the button disabled with a clear reason.
  const blockingMigration = useMemo(() => {
    const list = migrations.data ?? [];
    return list.find(
      (m) => m.status === 'running' && (m.source_host === vm.host_id || m.dest_host === effectiveTarget),
    );
  }, [migrations.data, vm.host_id, effectiveTarget]);

  const submit = () => {
    if (!target || !allOk) return;
    const params: Record<string, string> = { destination: target.ip };
    if (mode === 'live') {
      params.copy_storage = copyStorage ? 'true' : 'false';
    } else {
      params.keep_raw = keepRaw ? 'true' : 'false';
    }
    // Operator-pinned transport. Empty string = let the server auto-
    // pick based on host mTLS-readiness.
    if (transport) params.migration_transport = transport;
    if (mode === 'offline') {
      if (targetPool)  params.target_pool  = targetPool;
      if (newVMName && newVMName !== vm.name) params.new_vm_name = newVMName;
      if (regenerateMAC) params.regenerate_mac = 'true';
    }
    const startedAt = Date.now();
    setSubmittedAt(startedAt);
    cmd.mutate(
      {
        host_id: vm.host_id,
        vm_uuid: vm.uuid,
        vm_name: vm.name,
        action: mode === 'live' ? 'MIGRATE' : 'OFFLINE_MIGRATE',
        params,
      },
      {
        onError: () => {
          // Server rejected (e.g. 409 — another migration on this host).
          // Drop back to the form so the toast is the visible feedback.
          setSubmittedAt((cur) => (cur === startedAt ? null : cur));
        },
      },
    );
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
        onClose={sendToBackground}
        width={520}
        title={`Migrating — ${vm.name}`}
        footer={
          <button className="btn-secondary-sm" onClick={sendToBackground}>
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
          "Run in background" sends you to the Migrations page where you can keep
          watching progress. The migration continues on the source agent regardless.
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
          <button
            className="btn-primary-sm"
            onClick={submit}
            disabled={!allOk || cmd.isPending || !!blockingMigration}
            title={blockingMigration
              ? `Blocked: ${blockingMigration.vm_name} is already migrating ${blockingMigration.source_host} → ${blockingMigration.dest_host}`
              : ''}
          >
            {cmd.isPending
              ? 'Migrating…'
              : blockingMigration
                ? 'Host busy'
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
              {h.mtls_ready ? ' · mTLS' : ''}
            </option>
          ))}
        </select>
      </Field>

      <Field label="Transport">
        <TransportToggle
          value={transport}
          sourceReady={!!sourceHost?.mtls_ready}
          targetReady={!!target?.mtls_ready}
          onChange={setTransport}
        />
      </Field>

      {mode === 'offline' && (
        <div style={{ marginTop: 14 }}>
          <button
            type="button"
            onClick={() => setAdvancedOpen((v) => !v)}
            style={{
              width: '100%',
              display: 'flex', alignItems: 'center', gap: 10,
              padding: '10px 14px',
              background: advancedOpen ? 'var(--surface-2, var(--bg-input))' : 'var(--bg-input)',
              border: '1px solid var(--border)',
              borderRadius: advancedOpen ? '6px 6px 0 0' : 6,
              borderBottom: advancedOpen ? '1px solid var(--border)' : '1px solid var(--border)',
              color: 'var(--text-strong)',
              fontSize: 13, fontWeight: 600,
              cursor: 'pointer',
              textAlign: 'left',
              transition: 'background 0.12s',
            }}
          >
            <span style={{
              display: 'inline-flex', width: 18, height: 18, alignItems: 'center', justifyContent: 'center',
              borderRadius: 4, background: 'var(--accent-subtle)', color: 'var(--accent)', fontSize: 11, fontWeight: 700,
            }}>
              {advancedOpen ? '−' : '+'}
            </span>
            <span>Advanced</span>
            <span style={{ fontWeight: 400, fontSize: 11, color: 'var(--text-muted)' }}>
              target pool · rename · regenerate MACs
            </span>
            {(targetPool && targetPool !== 'default') || newVMName || regenerateMAC ? (
              <span className="tone-pill accent" style={{ marginLeft: 'auto', fontSize: 10 }}>
                customised
              </span>
            ) : null}
          </button>
          {advancedOpen && (
            <div style={{
              padding: 14, border: '1px solid var(--border)', borderTop: 'none',
              borderRadius: '0 0 6px 6px', background: 'var(--bg-input)',
              display: 'grid', gap: 10,
            }}>
              <Field label="Target storage pool">
                {poolsQuery.isLoading && <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Loading pools from {effectiveTarget}…</div>}
                {poolsQuery.error && <div style={{ fontSize: 12, color: 'var(--danger-text, #F85149)' }}>Failed: {(poolsQuery.error as Error).message}</div>}
                {poolsQuery.data && (
                  <select
                    value={targetPool}
                    onChange={(e) => setTargetPool(e.target.value)}
                    style={selectStyle}
                  >
                    {poolsQuery.data.map((p) => (
                      <option key={p.name} value={p.name}>
                        {p.name} — {p.type}, {p.available_gb.toFixed(1)} GB free / {p.capacity_gb.toFixed(1)} GB ({p.path})
                      </option>
                    ))}
                  </select>
                )}
              </Field>

              <Field label="Rename VM on destination (optional)">
                <input
                  type="text"
                  value={newVMName}
                  onChange={(e) => setNewVMName(e.target.value)}
                  placeholder={vm.name}
                  style={{
                    padding: '6px 10px', fontSize: 13,
                    background: 'var(--bg-input)', border: '1px solid var(--border)',
                    borderRadius: 4, color: 'var(--text-body)', fontFamily: 'inherit',
                  }}
                />
              </Field>

              <label style={{ ...checkboxLabel, alignSelf: 'flex-start' }}>
                <input
                  type="checkbox"
                  checked={regenerateMAC}
                  onChange={(e) => setRegenerateMAC(e.target.checked)}
                />
                <span>
                  <strong>Regenerate MAC addresses</strong>{' '}
                  <span style={{ color: 'var(--text-faded)' }}>
                    — issues a fresh 52:54:00 MAC per interface. Use when cloning so the original VM keeps its network identity.
                  </span>
                </span>
              </label>
            </div>
          )}
        </div>
      )}

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

      {blockingMigration && (
        <div
          className="tone-panel warn"
          style={{ marginTop: 14, padding: '10px 12px', borderLeftWidth: 3, fontSize: 12 }}
        >
          <strong>Host is busy.</strong>{' '}
          <span style={{ fontWeight: 600 }}>{blockingMigration.vm_name}</span>{' '}
          is migrating {blockingMigration.source_host} → {blockingMigration.dest_host}{' '}
          ({blockingMigration.phase || 'starting'} · {blockingMigration.progress ?? 0}%).
          Wait for it to finish, then try again.
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

function TransportToggle({
  value, sourceReady, targetReady, onChange,
}: {
  value: '' | 'tls' | 'ssh';
  sourceReady: boolean;
  targetReady: boolean;
  onChange: (v: '' | 'tls' | 'ssh') => void;
}) {
  const autoChoice = sourceReady && targetReady ? 'mTLS' : 'SSH';
  const tlsAvailable = sourceReady && targetReady;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'inline-flex', borderRadius: 6, border: '1px solid var(--border)', overflow: 'hidden', alignSelf: 'flex-start' }}>
        <TransportTab label={`Auto (${autoChoice})`} active={value === ''} onClick={() => onChange('')} />
        <TransportTab
          label="mTLS (Recommended)"
          active={value === 'tls'}
          onClick={() => onChange('tls')}
          disabled={!tlsAvailable}
          tone="ok"
        />
        <TransportTab label="SSH (Legacy)" active={value === 'ssh'} onClick={() => onChange('ssh')} tone="warn" />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>
        {!tlsAvailable
          ? 'mTLS not available — both hosts must have an active hypervisor cert. Using SSH fallback.'
          : value === 'ssh'
            ? 'Legacy path via the shared kvm-migrator keypair.'
            : value === 'tls'
              ? 'qemu+tls:// with a server-signed one-time grant.'
              : 'Server auto-selects mTLS when both hosts are enrolled.'}
      </div>
    </div>
  );
}

function TransportTab({
  label, active, onClick, disabled, tone,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
  disabled?: boolean;
  tone?: 'ok' | 'warn';
}) {
  const bg = active
    ? tone === 'ok'
      ? 'var(--success-bg, #1E3D23)'
      : tone === 'warn'
        ? 'var(--warn-bg, #3b2e10)'
        : 'var(--accent-subtle)'
    : 'transparent';
  const color = disabled ? 'var(--text-faded)' : 'var(--text-strong)';
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      style={{
        padding: '5px 12px', fontSize: 11, fontWeight: 500,
        border: 'none', background: bg, color,
        cursor: disabled ? 'not-allowed' : 'pointer',
        borderRight: '1px solid var(--border)',
      }}
    >
      {label}
    </button>
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
