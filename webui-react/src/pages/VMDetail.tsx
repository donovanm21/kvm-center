import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, NavLink, useMatch, useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { ActionsMenu, type ActionGroup } from '@/components/ActionsMenu';
import { MigrateModal } from '@/components/MigrateModal';
import { Modal } from '@/components/Modal';
import { PaginationBar, usePagination } from '@/components/Pagination';
import { apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useKVMToolsLatest, useMigrations, useVMs } from '@/hooks/useInventory';
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
  // Route the screenshot through the server proxy so an HTTPS SPA can
  // render it without a mixed-content block — the agent stays plain
  // HTTP on the LAN. The cache-bust on the querystring makes the
  // browser re-fetch on every VMDetail render.
  const thumbURL =
    running && host?.ip && canConsole
      ? `/api/vms/screenshot?host=${encodeURIComponent(vm.host_id)}&vm=${encodeURIComponent(vm.name)}&_t=${Date.now()}`
      : null;

  const openConsole = () => {
    if (!canConsole) return;
    // v5.0.4+: server-proxied console. Opens a same-origin URL so the
    // browser's session cookie is sent automatically; the server
    // re-validates vm.console on every hit. If the operator shares this
    // URL in chat/Slack/email, the recipient's browser — lacking the
    // cookie — hits a 401 immediately. No HMAC material is exposed to
    // the client, and agents never see direct browser traffic.
    const url = `/api/console/${encodeURIComponent(vm.host_id)}/${encodeURIComponent(vm.name)}`;
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
          <KVMToolsPill vm={vm} />
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
      <div className="tone-icon-tile accent">{icon}</div>
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
      {vm.snapshots_blocked_reason ? (
        <>
          <KV
            label="Snapshots"
            valueNode={<span title={vm.snapshots_blocked_reason} style={{ color: '#D29922' }}>⚠ unsupported ({vm.snapshot_count})</span>}
          />
          <KV
            label="Snapshot block reason"
            valueNode={<span style={{ fontSize: 11, lineHeight: 1.4, color: 'var(--text-faded)' }}>{vm.snapshots_blocked_reason}</span>}
          />
        </>
      ) : (
        <KV label="Snapshots" value={`${vm.snapshot_count}`} />
      )}
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

/**
 * KVM tools status pill on the VM Summary. Shows the running version,
 * flips to a warning tone with ⚠ when the guest's reported version
 * lags the build the server is shipping. Whole pill is a Link to the
 * Update Center so a click takes the operator straight to the page
 * where they can queue an update.
 */
function KVMToolsPill({ vm }: { vm: VM }) {
  const latest = useKVMToolsLatest();
  const present = vm.guest_agent;
  const reported = vm.kvm_tools_version || '';
  const latestVer = latest.data?.version || '';
  const outdated = present && latestVer && reported && reported !== latestVer;

  const cls = !present ? 'muted' : outdated ? 'warn' : 'ok';
  const label = !present
    ? '● KVM TOOLS: not installed'
    : outdated
      ? `● KVM TOOLS: ${reported} ⚠ outdated`
      : `● KVM TOOLS: ${reported || (vm.guest_source ?? 'present')}`;
  const title = !present
    ? 'Install kvm-tools inside the guest to expose hostname, IP addresses, and filesystem usage. Click for the Update Center.'
    : outdated
      ? `Guest reports ${reported}, server ships ${latestVer}. Click to queue an update.`
      : 'kvm-tools is reporting. Click for the Update Center.';
  return (
    <Link
      to="/kvm-tools"
      className={`tone-pill ${cls}`}
      title={title}
      style={{ marginBottom: 10, gap: 6, textDecoration: 'none', textTransform: 'uppercase' }}
    >
      {label}
    </Link>
  );
}

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
  const { hasPerm } = useAuth();
  const canEdit = hasPerm('vm.edit');
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div className="panel">
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <h2 style={{ margin: 0 }}>System configuration</h2>
          <EditResourcesButton vm={vm} canEdit={canEdit} />
        </div>
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
              <tr><th>Target</th><th>Device</th><th>Format</th><th>Bus</th><th>Path</th><th>Capacity</th><th>Allocated</th><th style={{ width: 120 }}></th></tr>
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
                  <td style={{ textAlign: 'right' }}>
                    {d.device === 'cdrom' && <CDROMActions vm={vm} disk={d} canEdit={canEdit} />}
                  </td>
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

/**
 * EditResourcesButton opens a modal to mutate RAM + vCPU count via
 * the EDIT_VM agent command. libvirt honours --live + --config for
 * setmem/setvcpus when the guest balloon driver / CPU topology
 * permit; otherwise the agent falls back to --config-only and the
 * response reports "reboot to apply" so the operator knows.
 */
// BOOT_DEVICES / CPU_MODES / VIDEO_MODELS are the curated set we
// surface in dropdowns. Keeping them short + documented is better
// than letting operators type arbitrary strings — libvirt's error
// paths for typos are cryptic. Each value is documented in tooltip.
// Short labels fit the dropdown cleanly; fuller context moves to a
// helper line under each control so nothing gets truncated.
const BOOT_DEVICES = [
  { value: 'hd',      label: 'Hard disk' },
  { value: 'cdrom',   label: 'CD-ROM' },
  { value: 'network', label: 'Network (PXE)' },
];
const CPU_MODES = [
  { value: 'host-passthrough', label: 'host-passthrough' },
  { value: 'host-model',       label: 'host-model' },
  { value: 'custom',           label: 'custom' },
];
const CPU_MODE_HELP: Record<string, string> = {
  'host-passthrough': 'Best performance — blocks migration to hosts with a different CPU.',
  'host-model':       'Portable across similar CPUs.',
  'custom':           'Explicit CPU definition.',
};
const VIDEO_MODELS = [
  { value: 'virtio', label: 'virtio' },
  { value: 'qxl',    label: 'qxl' },
  { value: 'vga',    label: 'vga' },
  { value: 'cirrus', label: 'cirrus' },
  { value: 'none',   label: 'none' },
];
const VIDEO_MODEL_HELP: Record<string, string> = {
  virtio: 'Best 3D + resize; Linux + Windows have guest drivers.',
  qxl:    'SPICE — legacy default.',
  vga:    'Low-compat fallback.',
  cirrus: 'Ancient guests only.',
  none:   'Serial only / no graphical output.',
};

function EditResourcesButton({ vm, canEdit }: { vm: VM; canEdit: boolean }) {
  const hostsQ = useHosts();
  const host = hostsQ.data?.find((h) => h.id === vm.host_id);
  const hostMaxVcpu = host?.cpu_count || 32;
  const hostMaxMemMB = host?.mem_total_mb || 65536;

  const [open, setOpen] = useState(false);
  const [vcpus, setVcpus] = useState<number>(vm.cpu_count || 1);
  const [memMB, setMemMB] = useState<number>(vm.mem_total_mb || 1024);
  const [bootDev, setBootDev] = useState<string>(vm.boot_dev || '');
  const [cpuMode, setCpuMode] = useState<string>(vm.cpu_mode || '');
  const [videoModel, setVideoModel] = useState<string>(vm.video_model || '');
  const [autostart, setAutostart] = useState<boolean>(!!vm.autostart);
  const [rebootPrompt, setRebootPrompt] = useState<string | null>(null);
  const [capRaisePrompt, setCapRaisePrompt] = useState<Record<string, string> | null>(null);
  const cmd = useVMCommand();

  const isRunning = vm.state === 'running';
  // XML-edited fields require the VM to be stopped — agent refuses
  // the edit otherwise. Disable them in the form with helper text.
  const offlineOnlyDisabled = isRunning;

  // Reset fields when the modal reopens so a cancelled edit doesn't
  // leave stale values in the form.
  useEffect(() => {
    if (open) {
      setVcpus(vm.cpu_count || 1);
      setMemMB(vm.mem_total_mb || 1024);
      setBootDev(vm.boot_dev || '');
      setCpuMode(vm.cpu_mode || '');
      setVideoModel(vm.video_model || '');
      setAutostart(!!vm.autostart);
    }
  }, [open, vm.cpu_count, vm.mem_total_mb, vm.boot_dev, vm.cpu_mode, vm.video_model, vm.autostart]);

  const submit = () => {
    const params: Record<string, string> = {};
    if (vcpus !== vm.cpu_count) params.vcpus = String(vcpus);
    if (memMB !== vm.mem_total_mb) params.mem_mb = String(memMB);
    if (bootDev && bootDev !== vm.boot_dev) params.boot_dev = bootDev;
    if (cpuMode && cpuMode !== vm.cpu_mode) params.cpu_mode = cpuMode;
    if (videoModel && videoModel !== vm.video_model) params.video_model = videoModel;
    if (autostart !== !!vm.autostart) params.autostart = autostart ? 'true' : 'false';
    if (Object.keys(params).length === 0) { setOpen(false); return; }
    cmd.mutate({
      host_id: vm.host_id,
      vm_uuid: vm.uuid,
      vm_name: vm.name,
      action: 'EDIT_VM',
      params,
    }, {
      onSuccess: (data: any) => {
        setOpen(false);
        // Agent signals that raising the vCPU cap needs a shutdown. Offer
        // a one-click "shutdown + apply + start" retry with
        // allow_shutdown=true so the operator doesn't have to manually
        // stop the VM.
        const msg: string = data?.message || data?.error || '';
        if (!data?.success && msg.includes("vCPU cap while it's running")) {
          setCapRaisePrompt(params);
          return;
        }
        if (data?.requires_reboot) {
          setRebootPrompt(data?.message || 'The change was saved to the VM config but requires a restart to take effect.');
        }
      },
    });
  };

  const capRaiseWithShutdown = () => {
    if (!capRaisePrompt) return;
    cmd.mutate({
      host_id: vm.host_id,
      vm_uuid: vm.uuid,
      vm_name: vm.name,
      action: 'EDIT_VM',
      params: { ...capRaisePrompt, allow_shutdown: 'true' },
    }, { onSuccess: () => setCapRaisePrompt(null) });
  };

  const restartNow = () => {
    // POWER_CYCLE (shutdown + start) instead of RESTART (ACPI reboot)
    // so qemu is re-spawned and the config-only EDIT_VM changes like
    // setmaxmem / setvcpus --maximum actually take effect. RESTART left
    // qemu running with the old caps, which is why setting RAM to 4 GB
    // would revert to 2 GB after the "reboot" prompt.
    cmd.mutate({
      host_id: vm.host_id,
      vm_uuid: vm.uuid,
      vm_name: vm.name,
      action: 'POWER_CYCLE',
    }, { onSuccess: () => setRebootPrompt(null) });
  };

  return (
    <>
      <button
        type="button"
        className="btn-secondary-sm"
        disabled={!canEdit}
        title={canEdit ? 'Edit vCPU + memory (live if libvirt permits, else config + reboot)' : 'Requires vm.edit permission'}
        onClick={() => setOpen(true)}
      >
        Edit resources
      </button>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={`Edit resources — ${vm.name}`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={() => setOpen(false)}>Cancel</button>
            <button type="button" className="btn-primary-sm" onClick={submit} disabled={cmd.isPending}>
              {cmd.isPending ? 'Applying…' : 'Apply'}
            </button>
          </>
        }
      >
        <div style={{ display: 'grid', gap: 14, fontSize: 12 }}>
          <EditRow
            label="vCPUs"
            meta={`host has ${hostMaxVcpu} logical CPUs`}
          >
            <select value={vcpus}
              onChange={(e) => setVcpus(parseInt(e.target.value) || 1)}
              style={resourceInput}>
              {Array.from({ length: hostMaxVcpu }, (_, i) => i + 1).map((n) => (
                <option key={n} value={n}>{n}{n === 1 ? ' vCPU' : ' vCPUs'}</option>
              ))}
            </select>
          </EditRow>

          <EditRow
            label="Memory (MB)"
            meta={`host has ${hostMaxMemMB.toLocaleString()} MB`}
          >
            <input type="number" min={128} step={128} max={hostMaxMemMB} value={memMB}
              onChange={(e) => setMemMB(Math.min(parseInt(e.target.value) || 128, hostMaxMemMB))}
              style={resourceInput} />
            <div style={{ display: 'flex', gap: 4, marginTop: 6 }}>
              {[1024, 2048, 4096, 8192, 16384].filter((n) => n <= hostMaxMemMB).map((n) => (
                <button key={n} type="button" onClick={() => setMemMB(n)}
                  className="btn-secondary-sm"
                  style={{ fontSize: 10, padding: '2px 8px' }}>
                  {n >= 1024 ? `${n / 1024}G` : `${n}M`}
                </button>
              ))}
            </div>
          </EditRow>

          <EditRow
            label="Boot device"
            help={offlineOnlyDisabled ? 'VM must be shut off to change the boot device.' : undefined}
            disabled={offlineOnlyDisabled}
          >
            <select value={bootDev} onChange={(e) => setBootDev(e.target.value)}
              disabled={offlineOnlyDisabled} style={resourceInput}>
              {BOOT_DEVICES.map((b) => <option key={b.value} value={b.value}>{b.label}</option>)}
            </select>
          </EditRow>

          <EditRow
            label="CPU mode"
            help={offlineOnlyDisabled
              ? 'VM must be shut off to change the CPU mode.'
              : CPU_MODE_HELP[cpuMode]}
            disabled={offlineOnlyDisabled}
          >
            <select value={cpuMode} onChange={(e) => setCpuMode(e.target.value)}
              disabled={offlineOnlyDisabled} style={resourceInput}>
              {CPU_MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
            </select>
          </EditRow>

          <EditRow
            label="Video adapter"
            help={offlineOnlyDisabled
              ? 'VM must be shut off to change the video adapter.'
              : VIDEO_MODEL_HELP[videoModel]}
            disabled={offlineOnlyDisabled}
          >
            <select value={videoModel} onChange={(e) => setVideoModel(e.target.value)}
              disabled={offlineOnlyDisabled} style={resourceInput}>
              {VIDEO_MODELS.map((v) => <option key={v.value} value={v.value}>{v.label}</option>)}
            </select>
          </EditRow>

          <label style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 0', cursor: 'pointer' }}>
            <input type="checkbox" checked={autostart} onChange={(e) => setAutostart(e.target.checked)} />
            <span style={{ color: 'var(--text-body)' }}>Auto-start on hypervisor boot</span>
          </label>

          <div style={{ color: 'var(--text-faded)', fontSize: 11, lineHeight: 1.6, marginTop: 4, paddingTop: 10, borderTop: '1px solid var(--border-subtle)' }}>
            Firmware, machine type, and vTPM require VM re-creation and
            aren't exposed here. vCPU / memory / autostart apply live when
            possible; boot device / CPU mode / video adapter require the
            VM to be stopped. Config-only changes trigger a restart prompt
            after Apply.
          </div>
        </div>
      </Modal>
      {/* Follow-up modal: surfaces requires_reboot from the agent. */}
      <Modal
        open={rebootPrompt !== null}
        onClose={() => setRebootPrompt(null)}
        title={`Restart ${vm.name} to apply changes?`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={() => setRebootPrompt(null)}>
              Later
            </button>
            <button type="button" className="btn-primary-sm" onClick={restartNow} disabled={cmd.isPending}>
              {cmd.isPending ? 'Restarting…' : 'Restart now'}
            </button>
          </>
        }
      >
        <div style={{ fontSize: 12, lineHeight: 1.55, color: 'var(--text-body)' }}>
          {rebootPrompt}
          <br /><br />
          <span style={{ color: 'var(--text-faded)' }}>
            "Restart now" fully power-cycles the VM (shutdown → start) so the
            fresh qemu process picks up the new memory cap / vCPU max. The
            VM is unavailable during this cycle.
          </span>
        </div>
      </Modal>
      {/* Follow-up modal: raising vCPU cap requires the VM to be shut off. */}
      <Modal
        open={capRaisePrompt !== null}
        onClose={() => setCapRaisePrompt(null)}
        title={`Raise vCPU cap on ${vm.name}?`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={() => setCapRaisePrompt(null)}>
              Cancel
            </button>
            <button type="button" className="btn-primary-sm" onClick={capRaiseWithShutdown} disabled={cmd.isPending}>
              {cmd.isPending ? 'Working…' : 'Shut down, raise cap, restart'}
            </button>
          </>
        }
      >
        <div style={{ fontSize: 12, lineHeight: 1.55, color: 'var(--text-body)' }}>
          Raising the vCPU count above the VM's current cap requires the VM
          to be shut off (libvirt constraint — <code>setvcpus --maximum</code>
          only works with <code>--config</code> on a stopped domain).
          <br /><br />
          The agent can do it in one step: graceful shutdown → raise cap →
          apply count → start. Expect ~10–30&nbsp;s of downtime.
        </div>
      </Modal>
    </>
  );
}

/**
 * CDROMActions exposes Eject (if the drive is currently loaded) and
 * Insert (pick an ISO from any pool) via the CHANGE_MEDIA command.
 * Shown only when vm.edit is carried; shown only for device="cdrom"
 * rows in the disks table.
 */
function CDROMActions({ vm, disk, canEdit }: { vm: VM; disk: VMDisk; canEdit: boolean }) {
  const cmd = useVMCommand();
  const [insertOpen, setInsertOpen] = useState(false);
  const [isoPath, setIsoPath] = useState('');

  const eject = () => {
    if (!confirm(`Eject ISO from ${disk.target} on ${vm.name}?`)) return;
    cmd.mutate({
      host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name,
      action: 'CHANGE_MEDIA',
      params: { media_action: 'eject', target: disk.target },
    });
  };
  const insert = () => {
    if (!isoPath.trim()) return;
    cmd.mutate({
      host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name,
      action: 'CHANGE_MEDIA',
      params: { media_action: 'insert', target: disk.target, path: isoPath.trim() },
    }, { onSuccess: () => { setInsertOpen(false); setIsoPath(''); } });
  };

  const loaded = !!disk.path;
  return (
    <div style={{ display: 'inline-flex', gap: 4, justifyContent: 'flex-end' }}>
      {loaded ? (
        <button type="button" className="btn-secondary-sm" disabled={!canEdit || cmd.isPending}
          title={canEdit ? 'Eject current ISO (live)' : 'Requires vm.edit'}
          onClick={eject}>Eject</button>
      ) : (
        <button type="button" className="btn-secondary-sm" disabled={!canEdit || cmd.isPending}
          title={canEdit ? 'Insert an ISO (live)' : 'Requires vm.edit'}
          onClick={() => setInsertOpen(true)}>Insert</button>
      )}
      <Modal
        open={insertOpen}
        onClose={() => { setInsertOpen(false); setIsoPath(''); }}
        title={`Insert ISO — ${vm.name} (${disk.target})`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={() => { setInsertOpen(false); setIsoPath(''); }}>Cancel</button>
            <button type="button" className="btn-primary-sm" onClick={insert} disabled={cmd.isPending || !isoPath.trim()}>
              {cmd.isPending ? 'Inserting…' : 'Insert'}
            </button>
          </>
        }
      >
        <div style={{ display: 'grid', gap: 8, fontSize: 12 }}>
          <div style={{ color: 'var(--text-muted)' }}>
            Full path to the ISO file on the hypervisor. Must be reachable
            by libvirt (usually inside a pool, e.g.
            <code style={{ margin: '0 4px', color: 'var(--text-strong)' }}>
              /var/lib/libvirt/images/ubuntu-24.04.iso
            </code>).
          </div>
          <input type="text"
            placeholder="/var/lib/libvirt/images/..."
            value={isoPath}
            onChange={(e) => setIsoPath(e.target.value)}
            autoFocus
            style={{ ...resourceInput, fontFamily: "'SF Mono', monospace" }} />
        </div>
      </Modal>
    </div>
  );
}

const resourceInput: React.CSSProperties = {
  padding: '6px 10px',
  fontSize: 13,
  background: 'var(--surface-2)',
  color: 'var(--text-strong)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  width: '100%',
  boxSizing: 'border-box',
};

/**
 * EditRow is the shared row layout for the Edit Resources modal —
 * one label + one control + optional helper text, all stacked
 * vertically for a consistent look that doesn't truncate on
 * narrow modal widths. A disabled row dims the control + label.
 */
function EditRow({
  label, meta, help, disabled, children,
}: {
  label: string;
  meta?: string;
  help?: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div style={{ display: 'grid', gap: 4, opacity: disabled ? 0.55 : 1 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, color: 'var(--text-muted)' }}>
        <span style={{ fontWeight: 500 }}>{label}</span>
        {meta && (
          <span style={{ color: 'var(--text-faded)', fontSize: 10 }}>{meta}</span>
        )}
      </div>
      {children}
      {help && (
        <div style={{ color: 'var(--text-faded)', fontSize: 11, lineHeight: 1.4 }}>
          {help}
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
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginBottom: 8 }}>
            Click any address to copy it to the clipboard.
          </div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {guestIPs.map((ip) => <IPBadge key={ip} ip={ip} />)}
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

const GUEST_KIND_TONE: Record<string, { tone: 'ok' | 'warn' | 'danger' | 'info' | 'muted'; label: string }> = {
  primary:   { tone: 'ok',    label: 'PRIMARY' },
  docker:    { tone: 'info',  label: 'DOCKER' },
  container: { tone: 'info',  label: 'CONTAINER' },
  vpn:       { tone: 'warn',  label: 'VPN' },
  bridge:    { tone: 'muted', label: 'BRIDGE' },
  other:     { tone: 'muted', label: 'OTHER' },
};
function GuestKindPill({ kind }: { kind: string }) {
  const t = GUEST_KIND_TONE[kind] ?? GUEST_KIND_TONE.other;
  return <span className={`tone-pill ${t.tone}`}>{t.label}</span>;
}

/**
 * Compact IP renderer — shows the first 2 addresses as clickable
 * copy-to-clipboard badges and collapses the rest behind a "+N more"
 * pill that toggles the full list.
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
      {show.map((ip) => <IPBadge key={ip} ip={ip} />)}
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

/**
 * Clickable IP box that copies the address to clipboard on click and
 * flashes a "copied" state. navigator.clipboard needs a secure context
 * (HTTPS or localhost); the kvm-center UI typically serves over plain
 * HTTP on the LAN, so we fall back to a legacy textarea + execCommand
 * path that works anywhere.
 */
export function IPBadge({ ip }: { ip: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const flash = (s: 'copied' | 'failed') => {
    setState(s);
    setTimeout(() => setState('idle'), 1400);
  };
  const copy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(ip);
        flash('copied');
        return;
      } catch { /* fall through */ }
    }
    try {
      const ta = document.createElement('textarea');
      ta.value = ip;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      flash(ok ? 'copied' : 'failed');
    } catch {
      flash('failed');
    }
  };
  const label = state === 'copied' ? '✓ copied' : state === 'failed' ? '✕ copy failed' : ip;
  const cls = state === 'copied' ? 'copied' : state === 'failed' ? 'failed' : '';
  return (
    <button
      type="button"
      onClick={copy}
      className={`ip-badge ${cls}`.trim()}
      title={state === 'failed' ? 'Browser blocked the copy — select the text manually.' : `Copy ${ip} to clipboard`}
    >
      {label}
    </button>
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

  const pg = usePagination(snaps.data ?? []);

  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <h2 style={{ margin: 0, padding: '16px 20px 10px', fontSize: 15 }}>Snapshots</h2>
      {snaps.isLoading && <div style={{ padding: '0 20px 16px', color: 'var(--text-muted)', fontSize: 12 }}>Loading…</div>}
      {snaps.error && (
        <div style={{ padding: '0 20px 16px', color: '#F85149', fontSize: 12 }}>
          Failed to load snapshots: {(snaps.error as Error).message}
        </div>
      )}
      {snaps.data && snaps.data.length === 0 && (
        <div style={{ padding: '0 20px 16px', color: 'var(--text-muted)', fontSize: 12 }}>No snapshots yet.</div>
      )}
      {snaps.data && snaps.data.length > 0 && (
        <>
        <div className="data-table-scroll">
        <table className="data-table">
          <thead>
            <tr><th>Snapshot</th><th>Created</th><th>State</th><th>Parent</th><th>Actions</th></tr>
          </thead>
          <tbody>
            {pg.items.map((s: Snapshot) => (
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
        </div>
        <PaginationBar pagination={pg} noun="snapshots" />
        </>
      )}
    </div>
  );
}

/* ── Create Snapshot modal ──────────────────────────────── */
export function CreateSnapshotModal({ vm, open, onClose }: { vm: VM; open: boolean; onClose: () => void }) {
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
const codeChip: React.CSSProperties = {
  background: 'var(--bg-input)', padding: '1px 6px', borderRadius: 3,
};
const diskListStyle: React.CSSProperties = {
  margin: '8px 0 0 0', padding: '8px 12px', listStyle: 'none',
  maxHeight: 160, overflowY: 'auto',
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4,
};
