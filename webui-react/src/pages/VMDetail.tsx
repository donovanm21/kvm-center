import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, NavLink, useMatch, useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { ActionsMenu, type ActionGroup } from '@/components/ActionsMenu';
import { MigrateModal } from '@/components/MigrateModal';
import { Modal } from '@/components/Modal';
import { apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useMigrations, useVMs } from '@/hooks/useInventory';
import { useToast } from '@/hooks/useToast';
import {
  useCreateSnapshot,
  useDeleteSnapshot,
  useRevertSnapshot,
  useSnapshots,
} from '@/hooks/useSnapshots';
import { useVMCommand, type VMAction } from '@/hooks/useVMCommand';
import type { GuestFS, Host, Snapshot, VM, VMDisk, VMNetwork, VMPCIDevice } from '@/types';

type ModalKind = null | 'snapshot' | 'delete' | 'migrate';

export function VMDetail() {
  const { uuid } = useParams<{ uuid: string }>();
  const vms = useVMs();
  const hosts = useHosts();

  const vm = useMemo(() => (vms.data ?? []).find((v) => v.uuid === uuid), [vms.data, uuid]);
  const host = useMemo(() => (hosts.data ?? []).find((h) => h.id === vm?.host_id), [hosts.data, vm]);
  const [modal, setModal] = useState<ModalKind>(null);

  if (vms.isLoading) return <div style={{ color: 'var(--text-muted)' }}>Loading…</div>;
  if (!vm) {
    return (
      <div className="panel">
        <h2>VM not found</h2>
        <p style={{ color: 'var(--text-muted)', fontSize: 12, margin: 0 }}>
          The requested VM isn't in the current inventory. It may have been deleted or migrated.
        </p>
      </div>
    );
  }

  return (
    <>
      <DetailHeader vm={vm} openModal={setModal} />
      <Tabs uuid={vm.uuid} />
      <TabBody vm={vm} host={host} />
      <CreateSnapshotModal vm={vm} open={modal === 'snapshot'} onClose={() => setModal(null)} />
      <DeleteVMModal vm={vm} open={modal === 'delete'} onClose={() => setModal(null)} />
      <MigrateModal
        vm={vm}
        hosts={hosts.data ?? []}
        open={modal === 'migrate'}
        onClose={() => setModal(null)}
      />
    </>
  );
}

function DetailHeader({ vm, openModal }: { vm: VM; openModal: (k: ModalKind) => void }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const migrations = useMigrations();
  const cmd = useVMCommand();

  // Lock all actions when this VM is mid-migration on the source host or
  // when a recent failed rollback may have left artifacts on the dest.
  // Same gate used by the VMs list — VMDetail is reachable by deep link
  // even for a VM that's currently locked in the list.
  const lock = useMemo(() => {
    for (const r of migrations.data ?? []) {
      if (r.vm_name !== vm.name) continue;
      if (r.status === 'running' && r.source_host === vm.host_id) {
        return { reason: 'migrating' as const, message: `Migration in progress (${r.phase || 'starting'} · ${r.progress ?? 0}%) — actions are disabled until it finishes.` };
      }
      if (r.status === 'failed' && r.dest_host === vm.host_id && ['define', 'start', 'verify', 'cleanup'].includes(r.phase ?? '')) {
        return { reason: 'rolled-back' as const, message: 'Migration rolled back — destination may have partial artifacts. Investigate before acting.' };
      }
    }
    return null;
  }, [migrations.data, vm.name, vm.host_id]);

  const gate = (perm: string, featFlag?: keyof NonNullable<typeof features.data>): { ok: boolean; reason?: string } => {
    if (lock) return { ok: false, reason: lock.message };
    if (!hasPerm(perm)) return { ok: false, reason: `Requires the ${perm} permission` };
    if (featFlag && features.data?.[featFlag]) return { ok: false, reason: 'Feature disabled by administrator' };
    return { ok: true };
  };
  const life = gate('vm.lifecycle');
  const snap = gate('vm.snapshot', 'disable_snapshots');
  const mig = gate('vm.migrate', 'disable_migrations');
  const del = gate('vm.delete', 'disable_vm_delete');

  const running = vm.state === 'running';
  const stopped = vm.state === 'stopped';
  const run = (action: VMAction) => () =>
    cmd.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, action });

  const groups: ActionGroup[] = [
    {
      items: [
        { label: '▶ Start',   enabled: stopped && life.ok, tooltip: life.reason, onSelect: run('START') },
        { label: '■ Shutdown',enabled: running && life.ok, tooltip: life.reason, onSelect: run('STOP') },
        { label: '↺ Restart', enabled: running && life.ok, tooltip: life.reason, onSelect: run('RESTART') },
        { label: '⚡ Force Off', danger: true, enabled: running && life.ok, tooltip: life.reason, onSelect: run('FORCESTOP') },
      ],
    },
    {
      items: [
        { label: '📷 Snapshot', enabled: snap.ok, tooltip: snap.reason, onSelect: () => openModal('snapshot') },
        { label: '↗ Migrate',   enabled: mig.ok, tooltip: mig.reason, onSelect: () => openModal('migrate') },
      ],
    },
    {
      items: [
        { label: '🗑 Delete VM', danger: true, enabled: del.ok, tooltip: del.reason, onSelect: () => openModal('delete') },
      ],
    },
  ];

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
      <div style={{ fontSize: 18, fontWeight: 600, color: 'var(--text-strong)' }}>{vm.name}</div>
      <span className={`state-pill ${vm.state}`}>{vm.state}</span>
      {lock && (
        <span
          className={`lock-pill ${lock.reason === 'rolled-back' ? 'danger' : 'warn'}`}
          title={lock.message}
          style={{ marginLeft: 0 }}
        >
          {lock.reason === 'rolled-back' ? 'rolled back' : 'migrating'}
        </span>
      )}
      <div style={{ flex: 1 }} />
      <ActionsMenu groups={groups} />
    </div>
  );
}

function Tabs({ uuid }: { uuid: string }) {
  return (
    <div style={tabBarStyle}>
      {[
        { to: '', label: 'Summary' },
        { to: 'monitor', label: 'Monitor' },
        { to: 'hardware', label: 'Hardware' },
        { to: 'networks', label: 'Networks' },
        { to: 'snapshots', label: 'Snapshots' },
      ].map((t) => (
        <NavLink
          key={t.to}
          to={t.to ? `/vms/${uuid}/${t.to}` : `/vms/${uuid}`}
          end
          style={({ isActive }) => ({
            ...tabStyle,
            color: isActive ? 'var(--text-strong)' : 'var(--text-muted)',
            borderBottomColor: isActive ? 'var(--accent)' : 'transparent',
          })}
        >
          {t.label}
        </NavLink>
      ))}
    </div>
  );
}

function TabBody({ vm, host }: { vm: VM; host: Host | undefined }) {
  const isMonitor = useMatch('/vms/:uuid/monitor');
  const isHardware = useMatch('/vms/:uuid/hardware');
  const isNetworks = useMatch('/vms/:uuid/networks');
  const isSnapshots = useMatch('/vms/:uuid/snapshots');
  if (isMonitor)   return <MonitorTab vm={vm} />;
  if (isHardware)  return <HardwareTab vm={vm} />;
  if (isNetworks)  return <NetworksTab vm={vm} />;
  if (isSnapshots) return <SnapshotsTab vm={vm} />;
  return <Summary vm={vm} host={host} />;
}

/* ── Summary ─────────────────────────────────────────────── */
function Summary({ vm, host }: { vm: VM; host: Host | undefined }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const canConsole = hasPerm('vm.console') && !features.data?.disable_console;
  const running = vm.state === 'running';
  const thumbURL =
    running && host?.ip && canConsole
      ? `http://${host.ip}:${host.agent_port || 8081}/screenshot/${encodeURIComponent(vm.name)}?_t=${Date.now()}`
      : null;

  const openConsole = () => {
    if (!host?.ip || !canConsole) return;
    const url = `http://${host.ip}:${host.agent_port || 8081}/console/${encodeURIComponent(vm.name)}`;
    window.open(url, '_blank', 'noopener,noreferrer');
  };

  const cpuPct = Math.min(vm.cpu_usage || 0, 100);
  const memPct = vm.mem_total_mb > 0 ? (vm.mem_usage_mb / vm.mem_total_mb) * 100 : 0;
  const diskPct = vm.disk_total_gb > 0 ? (vm.disk_usage_gb / vm.disk_total_gb) * 100 : 0;

  return (
    <div style={{ display: 'grid', gap: 16 }}>
      {/* Three-column layout: thumbnail · guest card · usage stack */}
      <div style={{ display: 'grid', gridTemplateColumns: '260px 1fr 320px', gap: 16, alignItems: 'stretch' }}>
        <div style={{ display: 'flex', flexDirection: 'column', minHeight: 0 }}>
          <div
            onClick={thumbURL ? openConsole : undefined}
            style={{
              position: 'relative',
              flex: 1,
              minHeight: 160,
              background: '#000',
              border: '1px solid var(--border)',
              borderRadius: 6,
              overflow: 'hidden',
              cursor: thumbURL ? 'pointer' : 'default',
            }}
          >
            {thumbURL ? (
              <img
                src={thumbURL}
                alt=""
                style={{ width: '100%', height: '100%', objectFit: 'contain' }}
                onError={(e) => {
                  (e.currentTarget as HTMLImageElement).style.display = 'none';
                }}
              />
            ) : (
              <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                <MonitorIcon />
              </div>
            )}
            <span
              style={{
                position: 'absolute', left: 8, bottom: 8,
                fontSize: 10, color: 'var(--text-muted)',
                textTransform: 'uppercase', letterSpacing: '0.08em', fontWeight: 600,
              }}
            >
              {vm.state}
            </span>
          </div>
          <button
            type="button"
            onClick={openConsole}
            disabled={!thumbURL}
            style={launchBtn}
            title={canConsole ? '' : 'Remote console disabled or missing permission'}
          >
            🖥 Launch Console
          </button>
        </div>

        <div className="panel">
          {vm.guest_agent ? (
            <div style={toolsPillStyle}>
              ● KVM TOOLS: {vm.guest_source ?? 'present'}
            </div>
          ) : (
            <div style={toolsPillMissingStyle} title="Install kvm-tools (or qemu-guest-agent) inside the guest to expose hostname, IP addresses, and filesystem usage.">
              ● KVM TOOLS: not installed
            </div>
          )}
          <KV label="Guest OS" value={vm.guest_os || '—'} />
          <KV label="Hostname" value={vm.guest_hostname || '—'} />
          <KV label="Kernel" value={vm.guest_kernel || '—'} />
          <KV
            label="IP Addresses"
            valueNode={<IPList ips={vm.guest_ips ?? []} />}
            mono
          />
          <KV
            label="Host"
            valueNode={
              <Link to={`/hosts/${encodeURIComponent(vm.host_id)}`} style={{ color: 'var(--accent)' }}>
                {vm.host_id}
                {host?.ip && <span style={{ color: 'var(--text-faded)', marginLeft: 6 }}>({host.ip})</span>}
              </Link>
            }
          />
          <KV label="UUID" value={vm.uuid} mono />
          <KV
            label="Power State"
            valueNode={<span className={`state-pill ${vm.state}`}>{vm.state}</span>}
          />
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <UsageCard
            icon={<CPUIcon />}
            label="CPU Usage"
            value={`${cpuPct.toFixed(1)}%`}
          />
          <UsageCard
            icon={<MemIcon />}
            label="Memory Usage"
            value={vm.mem_total_mb > 0 ? `${vm.mem_usage_mb} MB` : '—'}
            sub={vm.mem_total_mb > 0 ? `/ ${(vm.mem_total_mb / 1024).toFixed(1)} GB (${memPct.toFixed(0)}%)` : undefined}
          />
          <UsageCard
            icon={<DiskIcon />}
            label="Storage Usage"
            value={vm.disk_total_gb > 0 ? `${vm.disk_usage_gb.toFixed(1)} GB` : '—'}
            sub={vm.disk_total_gb > 0 ? `/ ${vm.disk_total_gb.toFixed(0)} GB (${diskPct.toFixed(0)}%)` : undefined}
          />
        </div>
      </div>

      {/* VM hardware quick-view + Notes */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16 }}>
        <HardwareQuickView vm={vm} />
        <NotesPanel vm={vm} />
      </div>

      <RelatedObjects vm={vm} host={host} />
    </div>
  );
}

function UsageCard({ icon, label, value, sub }: { icon: React.ReactNode; label: string; value: string; sub?: string }) {
  return (
    <div className="panel" style={{ padding: '12px 14px', display: 'flex', alignItems: 'center', gap: 12 }}>
      <div style={{ width: 32, height: 32, flexShrink: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#2D1810', borderRadius: 4, color: '#E3571E' }}>
        {icon}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)' }}>
          {label}
        </div>
        <div style={{ fontSize: 17, color: 'var(--text-strong)', fontWeight: 400, lineHeight: 1.2 }}>{value}</div>
        {sub && <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
      </div>
    </div>
  );
}

function HardwareQuickView({ vm }: { vm: VM }) {
  const disk = vm.all_disks?.[0];
  return (
    <div className="panel">
      <h2>VM Hardware</h2>
      <KV label="CPUs" value={`${vm.cpu_count}`} />
      <KV label="Memory" value={vm.mem_total_mb > 0 ? `${(vm.mem_total_mb / 1024).toFixed(1)} GB` : '—'} />
      <KV label="Hard Disk" value={disk?.path || '—'} mono />
      <KV label="Disk Size" value={vm.disk_total_gb > 0 ? `${vm.disk_total_gb.toFixed(1)} GB` : '—'} />

      {vm.networks && vm.networks.length > 0 && (
        <>
          <div style={sectionHeaderStyle}>Network Adapters</div>
          <table className="data-table" style={{ marginTop: 4 }}>
            <thead>
              <tr><th>Interface</th><th>Bridge</th><th>MAC</th><th>RX</th><th>TX</th></tr>
            </thead>
            <tbody>
              {vm.networks.map((n) => (
                <tr key={n.interface + n.mac}>
                  <td>{n.interface}</td>
                  <td>{n.bridge}</td>
                  <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11, color: 'var(--text-muted)' }}>{n.mac}</td>
                  <td>{fmtBytesShort(n.rx_bytes)}</td>
                  <td>{fmtBytesShort(n.tx_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}

      {vm.guest_fs && vm.guest_fs.length > 0 && (
        <>
          <div style={sectionHeaderStyle}>Guest Filesystems</div>
          <table className="data-table" style={{ marginTop: 4 }}>
            <thead>
              <tr><th>Mountpoint</th><th>Type</th><th>Used</th><th>Total</th><th>Usage</th></tr>
            </thead>
            <tbody>
              {vm.guest_fs.map((fs) => {
                const pct = fs.total_gb > 0 ? (fs.used_gb / fs.total_gb) * 100 : 0;
                const color = pct > 85 ? '#F85149' : pct > 65 ? '#FFB020' : '#3FB950';
                return (
                  <tr key={fs.mountpoint}>
                    <td style={{ fontFamily: "'SF Mono', monospace" }}>{fs.mountpoint}</td>
                    <td style={{ color: 'var(--text-muted)' }}>{fs.fstype}</td>
                    <td>{fs.used_gb.toFixed(1)} GB</td>
                    <td>{fs.total_gb.toFixed(1)} GB</td>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <div style={{ width: 80, height: 5, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
                          <div style={{ width: `${Math.min(pct, 100)}%`, height: '100%', background: color }} />
                        </div>
                        <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>{pct.toFixed(0)}%</span>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}

function NotesPanel({ vm }: { vm: VM }) {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const canEdit = hasPerm('vm.edit');
  const [draft, setDraft] = useState<string | null>(null);
  const editing = draft !== null;
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    if (editing) textareaRef.current?.focus();
  }, [editing]);

  const save = useMutation({
    mutationFn: (notes: string) => apiPost('/api/vms/notes', { uuid: vm.uuid, notes }),
    onSuccess: () => {
      toast('success', 'Notes saved');
      qc.invalidateQueries({ queryKey: ['vms'] });
      setDraft(null);
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast('error', `Failed to save notes: ${msg}`);
    },
  });

  return (
    <div className="panel">
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
        <h2 style={{ margin: 0 }}>Notes</h2>
        {!editing && (
          <button
            className="btn-secondary-sm"
            disabled={!canEdit}
            title={canEdit ? 'Edit notes' : 'Requires vm.edit'}
            onClick={() => setDraft(vm.notes ?? '')}
          >
            {vm.notes ? 'Edit' : 'Add'}
          </button>
        )}
      </div>
      {editing ? (
        <>
          <textarea
            ref={textareaRef}
            value={draft ?? ''}
            onChange={(e) => setDraft(e.target.value)}
            rows={6}
            placeholder="Free-form notes about this VM…"
            style={{
              width: '100%',
              boxSizing: 'border-box',
              background: 'var(--bg-input)',
              color: 'var(--text-body)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: 8,
              fontSize: 12,
              fontFamily: 'inherit',
              lineHeight: 1.5,
              resize: 'vertical',
            }}
          />
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 8 }}>
            <button className="btn-secondary-sm" onClick={() => setDraft(null)} disabled={save.isPending}>
              Cancel
            </button>
            <button
              className="btn-primary-sm"
              onClick={() => save.mutate(draft ?? '')}
              disabled={save.isPending || (draft ?? '') === (vm.notes ?? '')}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </>
      ) : vm.notes ? (
        <div style={{ fontSize: 12, color: 'var(--text-body)', whiteSpace: 'pre-wrap', lineHeight: 1.5 }}>{vm.notes}</div>
      ) : (
        <div style={{ fontSize: 12, color: 'var(--text-muted)', fontStyle: 'italic' }}>
          No notes yet.
        </div>
      )}
    </div>
  );
}

function RelatedObjects({ vm, host }: { vm: VM; host: Host | undefined }) {
  // Pools reported for this host give the Related Objects side the same
  // at-a-glance view v3 had at the bottom of Summary.
  const pools = Array.from(new Set((vm.all_disks ?? []).map((d) => d.path).filter(Boolean))).slice(0, 0);
  void pools; // disk paths aren't pool names; skip until we wire the pool lookup
  return (
    <div className="panel">
      <h2>Related Objects</h2>
      <KV
        label="Host"
        valueNode={
          <Link to={`/hosts/${encodeURIComponent(vm.host_id)}`} style={{ color: 'var(--accent)' }}>
            {vm.host_id}
            {host?.ip && <span style={{ color: 'var(--text-faded)', marginLeft: 6 }}>({host.ip})</span>}
          </Link>
        }
      />
      <KV label="Snapshots" value={`${vm.snapshot_count}`} />
      <KV label="VM disks" value={`${(vm.all_disks ?? []).length}`} />
      <KV label="PCI devices" value={`${(vm.pci_devices ?? []).length}`} />
    </div>
  );
}

function fmtBytesShort(b: number): string {
  if (!b) return '0 B';
  if (b > 1e9) return `${(b / 1e9).toFixed(1)} GB`;
  if (b > 1e6) return `${(b / 1e6).toFixed(1)} MB`;
  if (b > 1e3) return `${(b / 1e3).toFixed(1)} kB`;
  return `${b} B`;
}

function CPUIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor">
      <path d="M5 0a.5.5 0 0 1 .5.5V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2A2 2 0 0 1 14 4h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14a2 2 0 0 1-2 2v1.5a.5.5 0 0 1-1 0V14h-1v1.5a.5.5 0 0 1-1 0V14H8v1.5a.5.5 0 0 1-1 0V14H6v1.5a.5.5 0 0 1-1 0V14a2 2 0 0 1-2-2H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2V8H.5a.5.5 0 0 1 0-1H2V6H.5a.5.5 0 0 1 0-1H2a2 2 0 0 1 2-2V.5A.5.5 0 0 1 5 0zm-1 4a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1V5a1 1 0 0 0-1-1H4zm2 2h3a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1z" />
    </svg>
  );
}
function MemIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor">
      <path d="M1 3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v1.5a.5.5 0 0 1-.5.5h-13a.5.5 0 0 1-.5-.5V3zm2.5 0a.5.5 0 0 0-1 0v1a.5.5 0 0 0 1 0V3zm3 0a.5.5 0 0 0-1 0v1a.5.5 0 0 0 1 0V3zm-5 6a1 1 0 0 0-1 1v3a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-3a1 1 0 0 0-1-1H1.5zM1 6a.5.5 0 0 1 .5-.5h13a.5.5 0 0 1 0 1h-13A.5.5 0 0 1 1 6z" />
    </svg>
  );
}
function DiskIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor">
      <path d="M8 0C5.243 0 3 .895 3 2v12c0 1.105 2.243 2 5 2s5-.895 5-2V2c0-1.105-2.243-2-5-2zm0 1c2.21 0 4 .672 4 1.5S10.21 4 8 4s-4-.672-4-1.5S5.79 1 8 1zm4 4.5V6c0 .828-1.79 1.5-4 1.5S4 6.828 4 6v-.5c.9.521 2.367.875 4 .875s3.1-.354 4-.875z" />
    </svg>
  );
}

const toolsPillStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 6,
  padding: '2px 10px',
  marginBottom: 10,
  background: '#1E3D23',
  color: '#3FB950',
  fontSize: 10,
  fontWeight: 600,
  borderRadius: 10,
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
};
const toolsPillMissingStyle: React.CSSProperties = {
  ...toolsPillStyle,
  background: 'var(--surface-3)',
  color: 'var(--text-faded)',
  cursor: 'help',
};
const sectionHeaderStyle: React.CSSProperties = {
  marginTop: 14,
  fontSize: 10,
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  color: 'var(--text-faded)',
};

/* ── Monitor ─────────────────────────────────────────────── */
function MonitorTab({ vm }: { vm: VM }) {
  const cpuPct = Math.min(vm.cpu_usage || 0, 100);
  const memPct = vm.mem_total_mb > 0 ? Math.min((vm.mem_usage_mb / vm.mem_total_mb) * 100, 100) : 0;
  const diskPct = vm.disk_total_gb > 0 ? Math.min((vm.disk_usage_gb / vm.disk_total_gb) * 100, 100) : 0;
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <BigPanel title="CPU" valueLabel={`${cpuPct.toFixed(1)}%`} subLabel={`${vm.cpu_count} vCPU`} pct={cpuPct} tone={toneFor(cpuPct)} />
      <BigPanel
        title="Memory"
        valueLabel={vm.mem_total_mb > 0 ? `${(vm.mem_usage_mb / 1024).toFixed(1)} / ${(vm.mem_total_mb / 1024).toFixed(0)} GB` : '—'}
        subLabel={`${memPct.toFixed(0)}% used`}
        pct={memPct}
        tone={toneFor(memPct)}
      />
      <BigPanel
        title="Disk (host view)"
        valueLabel={vm.disk_total_gb > 0 ? `${vm.disk_usage_gb.toFixed(1)} / ${vm.disk_total_gb.toFixed(0)} GB` : '—'}
        subLabel={`${diskPct.toFixed(0)}% allocated`}
        pct={diskPct}
        tone={toneFor(diskPct)}
      />
      {vm.guest_fs && vm.guest_fs.length > 0 && (
        <div className="panel">
          <h2>Guest filesystems</h2>
          <table className="data-table">
            <thead>
              <tr><th>Mountpoint</th><th>Type</th><th>Used</th><th>Total</th><th>Usage</th></tr>
            </thead>
            <tbody>
              {vm.guest_fs.map((fs: GuestFS) => {
                const pct = fs.total_gb > 0 ? (fs.used_gb / fs.total_gb) * 100 : 0;
                return (
                  <tr key={fs.mountpoint}>
                    <td style={{ fontFamily: "'SF Mono', monospace" }}>{fs.mountpoint}</td>
                    <td style={{ color: 'var(--text-muted)' }}>{fs.fstype}</td>
                    <td>{fs.used_gb.toFixed(1)} GB</td>
                    <td>{fs.total_gb.toFixed(1)} GB</td>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <ResourceBar pct={pct} tone={toneFor(pct)} width={120} />
                        <span style={{ fontSize: 11, color: 'var(--text-muted)', minWidth: 36 }}>{pct.toFixed(0)}%</span>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function toneFor(pct: number): 'ok' | 'warn' | 'danger' {
  if (pct > 85) return 'danger';
  if (pct > 65) return 'warn';
  return 'ok';
}

function BigPanel({ title, valueLabel, subLabel, pct, tone }: {
  title: string; valueLabel: string; subLabel: string; pct: number; tone: 'ok' | 'warn' | 'danger';
}) {
  return (
    <div className="panel">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
        <div style={{ fontSize: 12, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em' }}>{title}</div>
        <div style={{ fontSize: 22, fontWeight: 300, color: 'var(--text-strong)' }}>{valueLabel}</div>
      </div>
      <ResourceBar pct={pct} tone={tone} />
      <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 4 }}>{subLabel}</div>
    </div>
  );
}

function ResourceBar({ pct, tone, width }: { pct: number; tone: 'ok' | 'warn' | 'danger'; width?: number }) {
  const color = tone === 'danger' ? '#F85149' : tone === 'warn' ? '#FFB020' : '#3FB950';
  return (
    <div style={{ width: width ?? '100%', height: 6, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden', marginTop: 6 }}>
      <div style={{ width: `${Math.max(0, Math.min(100, pct)).toFixed(1)}%`, height: '100%', background: color, transition: 'width 0.3s' }} />
    </div>
  );
}

/* ── Hardware ────────────────────────────────────────────── */
function HardwareTab({ vm }: { vm: VM }) {
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div className="panel">
        <h2>System configuration</h2>
        <KV label="Firmware" value={vm.firmware ?? '—'} />
        <KV label="Machine type" value={vm.machine_type ?? '—'} mono />
        <KV label="CPU mode" value={vm.cpu_mode ?? '—'} />
        <KV label="vCPUs" value={`${vm.cpu_count}`} />
        <KV label="Memory" value={vm.mem_total_mb > 0 ? `${(vm.mem_total_mb / 1024).toFixed(1)} GB` : '—'} />
        <KV label="Boot device" value={vm.boot_dev ?? '—'} />
        <KV label="vTPM" value={vm.has_tpm ? vm.tpm_version || 'present' : '✕ Not configured'} />
        <KV label="Video adapter" value={vm.video_model ?? '—'} />
        <KV label="Auto start" value={vm.autostart ? '✓ Enabled' : '✕ Disabled'} />
      </div>
      <div className="panel">
        <h2>Storage devices</h2>
        {(vm.all_disks ?? []).length === 0 ? (
          <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>No disks reported.</div>
        ) : (
          <table className="data-table">
            <thead>
              <tr><th>Target</th><th>Device</th><th>Format</th><th>Bus</th><th>Path</th><th>Capacity</th><th>Allocated</th></tr>
            </thead>
            <tbody>
              {vm.all_disks!.map((d: VMDisk) => (
                <tr key={d.target + d.path}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{d.target}</td>
                  <td>{d.device}</td>
                  <td><FormatPill value={d.disk_type} /></td>
                  <td>{d.bus || '—'}</td>
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
                    title={d.path}
                  >
                    {d.path || '—'}
                  </td>
                  <td>{d.capacity_gb > 0 ? `${d.capacity_gb.toFixed(1)} GB` : '—'}</td>
                  <td>{d.alloc_gb > 0 ? `${d.alloc_gb.toFixed(1)} GB` : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {vm.pci_devices && vm.pci_devices.length > 0 && (
        <div className="panel">
          <h2>PCI passthrough</h2>
          <table className="data-table">
            <thead><tr><th>Address</th><th>Description</th></tr></thead>
            <tbody>
              {vm.pci_devices.map((p: VMPCIDevice) => (
                <tr key={p.source_addr}>
                  <td style={{ fontFamily: "'SF Mono', monospace", color: '#FF8A3C' }}>{p.source_addr}</td>
                  <td>{p.host_desc || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div style={{ marginTop: 10, fontSize: 11, color: '#ff9800' }}>
            ⚠ PCI passthrough prevents live migration — the VM must be shut down to move.
          </div>
        </div>
      )}
    </div>
  );
}

function FormatPill({ value }: { value: string }) {
  const highlight = value === 'qcow2';
  return (
    <span
      style={{
        display: 'inline-block',
        padding: '2px 8px',
        borderRadius: 3,
        fontSize: 10,
        fontWeight: 600,
        background: highlight ? '#1E3D23' : '#3d2a00',
        color: highlight ? '#4caf50' : '#ff9800',
      }}
    >
      {value || '—'}
    </span>
  );
}

/* ── Networks ────────────────────────────────────────────── */
function NetworksTab({ vm }: { vm: VM }) {
  const networks = vm.networks ?? [];
  const guestIPs = vm.guest_ips ?? [];
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div className="panel">
        <h2>Virtual network adapters</h2>
        {networks.length === 0 ? (
          <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>No adapters reported.</div>
        ) : (
          networks.map((n: VMNetwork) => (
            <div key={n.interface + n.mac} style={{ borderTop: '1px solid var(--border-subtle)', paddingTop: 10, marginTop: 10 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                <div>
                  <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)' }}>
                    {n.interface}
                  </div>
                  <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>{n.bridge}</div>
                </div>
                <span className="state-pill running" style={{ background: '#FFF4EC', color: '#C04512' }}>BRIDGED</span>
              </div>
              <KV label="Bridge" value={n.bridge} />
              <KV label="MAC" value={n.mac} mono />
              <KV label="Host-side tap" value={n.interface} />
              <KV label="RX / TX" value={`${fmtBytes(n.rx_bytes)} / ${fmtBytes(n.tx_bytes)}`} />
            </div>
          ))
        )}
      </div>
      {(vm.guest_interfaces?.length ?? 0) > 0 && (
        <GuestInterfacesPanel ifaces={vm.guest_interfaces!} />
      )}
      {guestIPs.length > 0 && (
        <div className="panel">
          <h2>Guest IP addresses</h2>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {guestIPs.map((ip) => (
              <span key={ip} style={ipPillStyle}>{ip}</span>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * Guest interface table — surfaces docker0, container veth pairs, VPN
 * tunnels, and any other NIC the in-guest kvm-tools daemon reports
 * beyond the primary uplink. Useful for spotting "this VM has Docker"
 * or "this VM is on the wireguard mesh" without SSHing in.
 */
function GuestInterfacesPanel({ ifaces }: { ifaces: NonNullable<VM['guest_interfaces']> }) {
  const nonPrimary = ifaces.filter((i) => i.kind !== 'primary');
  return (
    <div className="panel">
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12 }}>
        <h2 style={{ margin: 0 }}>Guest interfaces</h2>
        {nonPrimary.length > 0 && (
          <span style={{ fontSize: 11, color: '#FF8A3C' }}>
            {nonPrimary.length} extra ({Array.from(new Set(nonPrimary.map((i) => i.kind))).join(', ')})
          </span>
        )}
      </div>
      <table className="data-table">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Kind</th>
            <th>Addresses</th>
            <th>MAC</th>
          </tr>
        </thead>
        <tbody>
          {ifaces.map((i) => (
            <tr key={i.name + (i.mac ?? '')}>
              <td style={{ fontFamily: "'SF Mono', monospace", fontWeight: 500, color: 'var(--text-strong)' }}>{i.name}</td>
              <td><GuestKindPill kind={i.kind} /></td>
              <td><IPList ips={i.addrs ?? []} /></td>
              <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11, color: 'var(--text-muted)' }}>{i.mac || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const GUEST_KIND_TONE: Record<string, { bg: string; fg: string; label: string }> = {
  primary:   { bg: '#1E3D23', fg: '#3FB950', label: 'PRIMARY' },
  docker:    { bg: '#0F2A4A', fg: '#58A6FF', label: 'DOCKER' },
  container: { bg: '#21304A', fg: '#79C0FF', label: 'CONTAINER' },
  vpn:       { bg: '#3D2510', fg: '#FF8A3C', label: 'VPN' },
  bridge:    { bg: '#2D2D2D', fg: '#C9D1D9', label: 'BRIDGE' },
  other:     { bg: 'var(--surface-3)', fg: 'var(--text-muted)', label: 'OTHER' },
};
function GuestKindPill({ kind }: { kind: string }) {
  const tone = GUEST_KIND_TONE[kind] ?? GUEST_KIND_TONE.other;
  return (
    <span style={{
      display: 'inline-block', padding: '1px 8px', borderRadius: 8,
      fontSize: 9, fontWeight: 700, letterSpacing: '0.5px',
      background: tone.bg, color: tone.fg,
    }}>
      {tone.label}
    </span>
  );
}

/**
 * Compact IP renderer — shows the first 2 addresses inline and
 * collapses the rest behind a clickable "+N more" pill that toggles
 * the full list. Used both for Guest IPs in Summary and per-interface
 * Addresses in the Networks tab.
 */
function IPList({ ips, max = 2 }: { ips: string[]; max?: number }) {
  const [expanded, setExpanded] = useState(false);
  if (ips.length === 0) {
    return <span style={{ color: 'var(--text-faded)' }}>—</span>;
  }
  const show = expanded ? ips : ips.slice(0, max);
  const remaining = ips.length - show.length;
  return (
    <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
      {show.map((ip, i) => (
        <span key={ip}>
          <span style={{ fontFamily: "'SF Mono', monospace" }}>{ip}</span>
          {i < show.length - 1 && <span style={{ color: 'var(--text-faded)' }}>,</span>}
        </span>
      ))}
      {remaining > 0 && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          title={`Show all ${ips.length} addresses`}
          style={ipMoreBtnStyle}
        >
          … +{remaining} more
        </button>
      )}
      {expanded && ips.length > max && (
        <button
          type="button"
          onClick={() => setExpanded(false)}
          title="Collapse"
          style={ipMoreBtnStyle}
        >
          show less
        </button>
      )}
    </span>
  );
}

const ipMoreBtnStyle: React.CSSProperties = {
  background: 'transparent',
  border: '1px dashed var(--border)',
  color: 'var(--accent)',
  padding: '0 6px',
  height: 18,
  fontSize: 10,
  fontWeight: 600,
  borderRadius: 4,
  cursor: 'pointer',
  fontFamily: 'inherit',
};

function fmtBytes(b: number): string {
  if (!b) return '0 B';
  if (b > 1e9) return `${(b / 1e9).toFixed(2)} GB`;
  if (b > 1e6) return `${(b / 1e6).toFixed(1)} MB`;
  if (b > 1e3) return `${(b / 1e3).toFixed(1)} kB`;
  return `${b} B`;
}

/* ── Snapshots ───────────────────────────────────────────── */
function SnapshotsTab({ vm }: { vm: VM }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const snaps = useSnapshots(vm.host_id, vm.name);
  const revert = useRevertSnapshot();
  const del = useDeleteSnapshot();
  const canSnap = hasPerm('vm.snapshot') && !features.data?.disable_snapshots;

  return (
    <div className="panel">
      <h2>Snapshots</h2>
      {snaps.isLoading && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading…</div>}
      {snaps.error && (
        <div style={{ color: '#F85149', fontSize: 12 }}>
          Failed to load snapshots: {(snaps.error as Error).message}
        </div>
      )}
      {snaps.data && snaps.data.length === 0 && (
        <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>No snapshots yet.</div>
      )}
      {snaps.data && snaps.data.length > 0 && (
        <table className="data-table">
          <thead>
            <tr><th>Snapshot</th><th>Created</th><th>State</th><th>Parent</th><th>Actions</th></tr>
          </thead>
          <tbody>
            {snaps.data.map((s: Snapshot) => (
              <tr key={s.name}>
                <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>
                  {s.name}{' '}
                  {s.is_current && (
                    <span
                      style={{
                        marginLeft: 8,
                        padding: '1px 6px',
                        fontSize: 9,
                        borderRadius: 10,
                        background: '#3D2510',
                        color: '#FF8A3C',
                        fontWeight: 600,
                        textTransform: 'uppercase',
                        letterSpacing: '0.5px',
                      }}
                    >
                      Current
                    </span>
                  )}
                </td>
                <td style={{ fontFamily: "'SF Mono', monospace", color: 'var(--text-muted)', fontSize: 11 }}>
                  {s.creation_time}
                </td>
                <td><span className={`state-pill ${s.state === 'running' ? 'running' : 'stopped'}`}>{s.state}</span></td>
                <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{s.parent || '—'}</td>
                <td style={{ display: 'flex', gap: 6 }}>
                  <button
                    type="button"
                    className="btn-primary-sm"
                    disabled={s.is_current || !canSnap}
                    title={canSnap ? (s.is_current ? 'Already current' : '') : 'Snapshots disabled or missing permission'}
                    onClick={() =>
                      revert.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name })
                    }
                  >
                    ↶ Revert
                  </button>
                  <button
                    type="button"
                    className="btn-danger-sm"
                    disabled={!canSnap}
                    onClick={() =>
                      del.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: s.name })
                    }
                  >
                    🗑 Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

/* ── Create Snapshot modal ──────────────────────────────── */
function CreateSnapshotModal({ vm, open, onClose }: { vm: VM; open: boolean; onClose: () => void }) {
  const [name, setName] = useState(() => defaultSnapshotName());
  const create = useCreateSnapshot();
  const submit = async () => {
    if (!name.trim()) return;
    await create.mutateAsync({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, name: name.trim() });
    onClose();
    setName(defaultSnapshotName());
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Create Snapshot — ${vm.name}`}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={!name.trim() || create.isPending}>
            {create.isPending ? 'Creating…' : 'Create Snapshot'}
          </button>
        </>
      }
    >
      <label style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em' }}>
        Name
      </label>
      <input
        autoFocus
        type="text"
        value={name}
        onChange={(e) => setName(e.target.value)}
        style={{
          width: '100%', marginTop: 6, height: 34,
          padding: '0 12px', border: '1px solid var(--border)', borderRadius: 4,
          background: 'var(--bg-input)', color: 'var(--text-body)', fontSize: 13,
          fontFamily: "'SF Mono', monospace",
        }}
      />
      <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text-faded)' }}>
        Snapshots only work on VMs with qcow2 disks.
      </div>
    </Modal>
  );
}

function defaultSnapshotName(): string {
  const d = new Date();
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `snap-${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}`;
}

/* ── Delete VM modal (3-stage confirm) ──────────────────── */
function DeleteVMModal({ vm, open, onClose }: { vm: VM; open: boolean; onClose: () => void }) {
  const nav = useNavigate();
  const cmd = useVMCommand();
  const [step, setStep] = useState(1);
  const [ack, setAck] = useState(false);
  const [deleteDisks, setDeleteDisks] = useState(false);
  const [typed, setTyped] = useState('');

  const reset = () => { setStep(1); setAck(false); setDeleteDisks(false); setTyped(''); };
  const close = () => { reset(); onClose(); };

  const submit = () => {
    cmd.mutate(
      {
        host_id: vm.host_id,
        vm_uuid: vm.uuid,
        vm_name: vm.name,
        action: 'DELETE_VM',
        params: { delete_disks: deleteDisks ? 'true' : 'false' },
      },
      {
        onSuccess: (d) => {
          if (d && (d as any).success === false) return;
          close();
          nav('/vms');
        },
      },
    );
  };

  const diskList = (vm.all_disks ?? []).filter((d) => d.path);

  return (
    <Modal
      open={open}
      onClose={close}
      title={<span style={{ color: '#F85149' }}>Delete VM — {vm.name}</span>}
      width={560}
      footer={
        step === 1 ? (
          <>
            <button className="btn-secondary-sm" onClick={close}>Cancel</button>
            <button className="btn-danger-sm" onClick={() => setStep(2)}>Continue</button>
          </>
        ) : step === 2 ? (
          <>
            <button className="btn-secondary-sm" onClick={() => setStep(1)}>Back</button>
            <button className="btn-danger-sm" disabled={!ack} onClick={() => setStep(3)}>
              I understand — continue
            </button>
          </>
        ) : (
          <>
            <button className="btn-secondary-sm" disabled={cmd.isPending} onClick={() => setStep(2)}>Back</button>
            <button
              className="btn-danger-sm"
              disabled={typed !== vm.name || cmd.isPending}
              onClick={submit}
            >
              {cmd.isPending ? 'Deleting…' : 'Delete VM'}
            </button>
          </>
        )
      }
    >
      {step === 1 && (
        <>
          <p style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.55 }}>
            This will <strong>permanently destroy</strong> the domain{' '}
            <strong>{vm.name}</strong> on{' '}
            <code style={codeChip}>{vm.host_id}</code>. The action can't be undone
            from this UI — restore from backup is the only recovery path.
          </p>
          <ol style={{ margin: '6px 0 0 20px', color: 'var(--text-body)', fontSize: 12, lineHeight: 1.7 }}>
            <li>Force-off if still running (<code style={codeChip}>virsh destroy</code>).</li>
            <li>Undefine the domain, including NVRAM, snapshot metadata, managed-save state.</li>
            <li>Remove these disk files:</li>
          </ol>
          <ul style={diskListStyle}>
            {diskList.length === 0 ? (
              <li style={{ color: 'var(--text-faded)', fontSize: 11, padding: '4px 0' }}>
                No disks reported — nothing extra to unlink.
              </li>
            ) : (
              diskList.map((d) => (
                <li key={d.path} style={{ fontFamily: "'SF Mono', monospace", fontSize: 11, color: 'var(--text-body)', padding: '4px 0', borderBottom: '1px solid var(--border)' }}>
                  {d.path} <span style={{ color: 'var(--text-faded)' }}>· {d.disk_type || d.device || ''} · {d.capacity_gb ? `${d.capacity_gb.toFixed(1)} GB` : ''}</span>
                </li>
              ))
            )}
          </ul>
        </>
      )}
      {step === 2 && (
        <>
          <p style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.55 }}>
            The domain definition, snapshots, NVRAM, and managed-save state
            are always removed — there is no trash bin. Choose what should
            happen to the backing disk files:
          </p>
          <label
            style={{
              marginTop: 12, display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer',
              padding: '10px 12px', background: 'var(--bg-input)',
              border: `1px solid ${deleteDisks ? '#A02929' : 'var(--border)'}`,
              borderLeft: `3px solid ${deleteDisks ? '#F85149' : 'var(--border)'}`,
              borderRadius: 4,
            }}
          >
            <input
              type="checkbox"
              checked={deleteDisks}
              onChange={(e) => setDeleteDisks(e.target.checked)}
              style={{ marginTop: 2 }}
            />
            <span style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.5 }}>
              <strong style={{ color: deleteDisks ? '#F85149' : 'var(--text-strong)' }}>
                Also delete the backing disk files
              </strong>
              <br />
              <span style={{ color: 'var(--text-faded)', fontSize: 11 }}>
                {deleteDisks
                  ? `The ${diskList.length} disk file${diskList.length === 1 ? '' : 's'} listed on the previous step will be permanently removed. This cannot be undone.`
                  : 'Leave unchecked to keep the disk files on the host so they can be reattached to a new VM later.'}
              </span>
            </span>
          </label>
          <label
            style={{
              marginTop: 10, display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer',
              padding: '10px 12px', background: 'var(--bg-input)',
              border: '1px solid var(--border)', borderRadius: 4,
            }}
          >
            <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} style={{ marginTop: 2 }} />
            <span style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.5 }}>
              I understand that the VM definition, snapshots, and metadata will
              be permanently removed
              {deleteDisks && ', and that the backing disk files will be deleted'}
              .
            </span>
          </label>
        </>
      )}
      {step === 3 && (
        <>
          <p style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.55 }}>
            To confirm, type the VM name{' '}
            <code style={{ ...codeChip, color: '#F85149' }}>{vm.name}</code> below.
          </p>
          <input
            autoFocus
            spellCheck={false}
            autoComplete="off"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            style={{
              marginTop: 10, width: '100%', height: 36, padding: '0 12px',
              background: 'var(--bg-input)', border: '1px solid var(--border)',
              borderRadius: 4, color: 'var(--text-body)', fontFamily: 'monospace',
            }}
          />
        </>
      )}
    </Modal>
  );
}

/* ── Shared bits ────────────────────────────────────────── */
function KV({
  label,
  value,
  valueNode,
  mono = false,
}: {
  label: string;
  value?: string;
  valueNode?: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div style={{ display: 'flex', padding: '6px 0', gap: 12 }}>
      <div style={{ width: 140, fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
      <div
        style={{
          fontSize: 12,
          color: 'var(--text-body)',
          fontFamily: mono ? "'SF Mono', monospace" : 'inherit',
          wordBreak: 'break-all',
        }}
      >
        {valueNode ?? value}
      </div>
    </div>
  );
}

function MonitorIcon() {
  return (
    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#FF8A3C" strokeWidth="1.5">
      <rect x="2" y="4" width="20" height="14" rx="2" />
      <path d="M8 22h8M12 18v4" />
    </svg>
  );
}

const tabBarStyle: React.CSSProperties = {
  display: 'flex',
  gap: 4,
  borderBottom: '1px solid var(--border)',
  marginBottom: 16,
};
const tabStyle: React.CSSProperties = {
  padding: '8px 16px',
  fontSize: 13,
  fontWeight: 500,
  textDecoration: 'none',
  borderBottom: '2px solid transparent',
};
const launchBtn: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 8,
  width: '100%',
  marginTop: 10,
  height: 34,
  padding: '0 14px',
  color: 'var(--accent)',
  fontSize: 12,
  fontWeight: 600,
  letterSpacing: '0.3px',
  border: '1px solid var(--accent)',
  borderRadius: 4,
  background: 'transparent',
  cursor: 'pointer',
  fontFamily: 'inherit',
};
const ipPillStyle: React.CSSProperties = {
  display: 'inline-block',
  padding: '6px 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  fontFamily: "'SF Mono', monospace",
  fontSize: 11,
  color: 'var(--text-body)',
};
const codeChip: React.CSSProperties = {
  background: 'var(--bg-input)', padding: '1px 6px', borderRadius: 3,
};
const diskListStyle: React.CSSProperties = {
  margin: '8px 0 0 0', padding: '8px 12px', listStyle: 'none',
  maxHeight: 160, overflowY: 'auto',
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4,
};
