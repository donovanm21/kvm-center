import { useMemo, useState } from 'react';
import { NavLink, useMatch, useNavigate, useParams } from 'react-router-dom';

import { ActionsMenu, type ActionGroup } from '@/components/ActionsMenu';
import { Modal } from '@/components/Modal';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useVMs } from '@/hooks/useInventory';
import {
  useCreateSnapshot,
  useDeleteSnapshot,
  useRevertSnapshot,
  useSnapshots,
} from '@/hooks/useSnapshots';
import { useToast } from '@/hooks/useToast';
import { useVMCommand, type VMAction } from '@/hooks/useVMCommand';
import type { GuestFS, Host, Snapshot, VM, VMDisk, VMNetwork, VMPCIDevice } from '@/types';

type ModalKind = null | 'snapshot' | 'delete';

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
    </>
  );
}

function DetailHeader({ vm, openModal }: { vm: VM; openModal: (k: ModalKind) => void }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const cmd = useVMCommand();
  const { toast } = useToast();

  const gate = (perm: string, featFlag?: keyof NonNullable<typeof features.data>): { ok: boolean; reason?: string } => {
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
        { label: '↗ Migrate',   enabled: mig.ok, tooltip: mig.reason, onSelect: () => toast('info', 'Migration modal ships in the next commit') },
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

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '260px 1fr', gap: 16 }}>
      <div>
        <div
          onClick={thumbURL ? openConsole : undefined}
          style={{
            position: 'relative',
            aspectRatio: '4 / 3',
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
        <h2>Guest details</h2>
        <KV label="Guest OS" value={vm.guest_os || '—'} />
        <KV label="Hostname" value={vm.guest_hostname || '—'} />
        <KV label="Host" value={vm.host_id} />
        <KV label="UUID" value={vm.uuid} mono />
        <KV label="Power state" value={vm.state} />
        <KV label="CPU" value={`${vm.cpu_count} vCPU · ${vm.cpu_usage.toFixed(1)}% usage`} />
        <KV
          label="Memory"
          value={vm.mem_total_mb > 0
            ? `${(vm.mem_usage_mb / 1024).toFixed(1)} / ${(vm.mem_total_mb / 1024).toFixed(0)} GB`
            : '—'}
        />
        <KV
          label="Disk"
          value={vm.disk_total_gb > 0
            ? `${vm.disk_usage_gb.toFixed(1)} / ${vm.disk_total_gb.toFixed(0)} GB (${vm.disk_type ?? 'qcow2'})`
            : '—'}
        />
        <KV label="Guest tools" value={vm.guest_agent ? (vm.guest_source ?? 'present') : 'not installed'} />
      </div>
    </div>
  );
}

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
        <h2>Storage devices ({(vm.all_disks ?? []).length})</h2>
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
          <h2>PCI passthrough ({vm.pci_devices.length})</h2>
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
        <h2>Virtual network adapters ({networks.length})</h2>
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
  const [typed, setTyped] = useState('');

  const reset = () => { setStep(1); setAck(false); setTyped(''); };
  const close = () => { reset(); onClose(); };

  const submit = () => {
    cmd.mutate(
      { host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, action: 'DELETE_VM' },
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
            The disk files listed on the previous step will be{' '}
            <strong>deleted from disk</strong>. Snapshots, NVRAM, and managed-save
            state are also removed — there is no trash bin.
          </p>
          <label
            style={{
              marginTop: 12, display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer',
              padding: '10px 12px', background: 'var(--bg-input)',
              border: '1px solid var(--border)', borderRadius: 4,
            }}
          >
            <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
            <span style={{ fontSize: 12, color: 'var(--text-body)', lineHeight: 1.5 }}>
              I understand that the VM's disks, snapshots, and all associated
              metadata will be permanently removed from the host and cannot be
              recovered.
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
function KV({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
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
        {value}
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
