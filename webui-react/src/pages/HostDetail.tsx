import { useMemo, useState } from 'react';
import { Link, NavLink, useMatch, useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { apiDelete, apiPost } from '@/api/client';
import { HostMetricsGraphs } from '@/components/HostMetricsGraphs';
import { KVMToolsPill } from '@/components/KVMToolsPill';
import { Modal } from '@/components/Modal';
import { PaginationBar, usePagination } from '@/components/Pagination';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useKVMToolsLatest, useMigrations, useStorage, useVMs } from '@/hooks/useInventory';
import { useToast } from '@/hooks/useToast';
import { useVMCommand } from '@/hooks/useVMCommand';
import type { Host, HostPCIDevice, StoragePool, VM } from '@/types';
import { CreateSnapshotModal } from '@/pages/VMDetail';
import { SortableTH, useSort } from '@/components/SortableTH';
import { PHASES_PAST_DEFINE, lockKey } from '@/pages/VMs';
import { LockBadge, StatePill, type LockReason } from '@/components/StatePill';
import { qk } from '@/queryKeys';

/**
 * MaintenanceToggle flips a host between operational and "draining"
 * state. Server refuses new-VM placements and migrations-in while the
 * flag is on; migrations OFF the host remain allowed. Recorded in
 * the audit log as host.maintenance.{enter,exit}.
 */
function MaintenanceToggle({ host }: { host: Host }) {
  const { hasPerm } = useAuth();
  const qc = useQueryClient();
  const { toast } = useToast();
  const [reason, setReason] = useState('');
  const [drain, setDrain] = useState(false);
  const [mode, setMode] = useState<'live' | 'offline'>('live');
  const [asking, setAsking] = useState(false);
  const canManage = hasPerm('host.manage');
  const on = !!host.maintenance_mode;

  const toggle = useMutation({
    mutationFn: () => apiPost(
      `/api/hosts/${encodeURIComponent(host.id)}/maintenance/${on ? 'exit' : 'enter'}`,
      on ? undefined : { reason, drain, mode },
    ),
    onSuccess: () => {
      toast(
        'success',
        on
          ? 'Host back in service'
          : drain
            ? `Host entered maintenance — draining VMs (${mode})`
            : 'Host entered maintenance mode',
      );
      qc.invalidateQueries({ queryKey: qk.hosts() });
      setAsking(false);
      setReason('');
      setDrain(false);
    },
    onError: (e) => toast('error', `Failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const close = () => { setAsking(false); setReason(''); setDrain(false); setMode('live'); };

  return (
    <>
      <button
        type="button"
        className={on ? 'btn-primary-sm' : 'btn-secondary-sm'}
        disabled={!canManage || toggle.isPending}
        title={!canManage ? 'Requires host.manage' : on ? 'Exit maintenance — allow new VMs + migration targets again' : 'Enter maintenance — refuse new placements on this host'}
        onClick={() => on ? toggle.mutate() : setAsking(true)}
      >
        {on ? 'Exit maintenance' : 'Enter maintenance'}
      </button>

      <Modal
        open={asking && !on}
        onClose={close}
        title={`Enter maintenance — ${host.id}`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={close}>Cancel</button>
            <button type="button" className="btn-primary-sm" onClick={() => toggle.mutate()} disabled={toggle.isPending}>
              {toggle.isPending ? 'Entering…' : drain ? 'Enter & drain' : 'Enter maintenance'}
            </button>
          </>
        }
      >
        <div style={{ display: 'grid', gap: 12 }}>
          <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text-muted)' }}>
            Reason
            <input
              type="text"
              autoFocus
              placeholder="e.g. kernel patch, hardware swap"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              style={{
                height: 30, padding: '0 10px', fontSize: 13,
                background: 'var(--bg-input)', border: '1px solid var(--border)',
                borderRadius: 4, color: 'var(--text-body)', fontFamily: 'inherit',
              }}
            />
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text-body)' }}>
            <input type="checkbox" checked={drain} onChange={(e) => setDrain(e.target.checked)} />
            Drain VMs to other hosts
          </label>
          {drain && (
            <div style={{ display: 'flex', gap: 16, fontSize: 13, color: 'var(--text-body)', paddingLeft: 24 }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <input type="radio" name="drain-mode" checked={mode === 'live'} onChange={() => setMode('live')} />
                Live (running VMs only)
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <input type="radio" name="drain-mode" checked={mode === 'offline'} onChange={() => setMode('offline')} />
                Offline (any state)
              </label>
            </div>
          )}
          {drain && (
            <div className="tone-pill" style={{ fontSize: 11, alignSelf: 'flex-start' }}>
              Migrations run sequentially in the background — watch progress in Migrations.
            </div>
          )}
        </div>
      </Modal>
    </>
  );
}

/**
 * UpdateAgentButton pushes the latest agent binary to this specific
 * hypervisor. Server proxies to the agent's /self-update endpoint,
 * which downloads from /downloads/kvm-center-agent-<arch>, replaces
 * its own binary on disk, and exits so systemd restarts it. Requires
 * host.manage.
 */
type UpdateAgentResponse = {
  ok?: boolean;
  message?: string;
  version?: string;
};

function UpdateAgentButton({ host }: { host: Host }) {
  const { hasPerm } = useAuth();
  const qc = useQueryClient();
  const { toast } = useToast();
  const [asking, setAsking] = useState(false);
  const canManage = hasPerm('host.manage');

  const mut = useMutation({
    mutationFn: () => apiPost<UpdateAgentResponse>(`/api/hosts/${encodeURIComponent(host.id)}/update-agent`),
    onSuccess: (data) => {
      toast('success', data?.message || `Agent update requested on ${host.id}`);
      setAsking(false);
      // Telemetry from the fresh binary lands 30s later; invalidate
      // hosts now + again after a short delay so the UI refresh
      // reflects any new agent-reported fields.
      qc.invalidateQueries({ queryKey: qk.hosts() });
      setTimeout(() => qc.invalidateQueries({ queryKey: qk.hosts() }), 10_000);
    },
    onError: (e) => toast('error', `Update failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  return (
    <>
      <button
        type="button"
        className="btn-secondary-sm"
        disabled={!canManage || mut.isPending}
        title={!canManage ? 'Requires host.manage' : 'Download the latest agent binary and restart the host agent (needs systemd)'}
        onClick={() => setAsking(true)}
      >
        {mut.isPending ? 'Updating…' : 'Update agent'}
      </button>
      <Modal
        open={asking}
        onClose={() => setAsking(false)}
        title={`Update agent on ${host.id}`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={() => setAsking(false)}>Cancel</button>
            <button type="button" className="btn-primary-sm" onClick={() => mut.mutate()} disabled={mut.isPending}>
              {mut.isPending ? 'Updating…' : 'Update agent'}
            </button>
          </>
        }
      >
        <div style={{ fontSize: 12, lineHeight: 1.55, color: 'var(--text-body)' }}>
          The host agent will download the latest binary from this server's
          <code style={{ margin: '0 4px' }}>/downloads/</code> endpoint, replace
          its own executable on disk, and exit so systemd restarts it.
          Expected downtime is &lt;10 seconds during which this host's telemetry
          stops and VMs on it can't be started/stopped. Running VMs are not
          affected.
        </div>
      </Modal>
    </>
  );
}

/**
 * DeleteHostButton removes the hypervisor from the inventory — host
 * record, VM rows, storage metadata, and enrolment cert. Physical
 * VMs on the hypervisor are untouched. Requires host.manage; the
 * server refuses while a migration references this host.
 */
function DeleteHostButton({ host }: { host: Host }) {
  const { hasPerm } = useAuth();
  const qc = useQueryClient();
  const { toast } = useToast();
  const [asking, setAsking] = useState(false);
  const [confirmName, setConfirmName] = useState('');
  const canManage = hasPerm('host.manage');

  const del = useMutation({
    mutationFn: () => apiDelete(`/api/hosts/${encodeURIComponent(host.id)}`),
    onSuccess: () => {
      toast('success', `Removed ${host.id} from inventory`);
      qc.invalidateQueries({ queryKey: qk.hosts() });
      qc.invalidateQueries({ queryKey: qk.vms() });
      setAsking(false);
      setConfirmName('');
      // Bounce back to the Hosts list since this page no longer has a subject.
      window.location.assign('/hosts');
    },
    onError: (e) => toast('error', `Failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const close = () => { setAsking(false); setConfirmName(''); };
  const canSubmit = confirmName.trim() === host.id && !del.isPending;

  return (
    <>
      <button
        type="button"
        className="btn-secondary-sm"
        style={{ color: '#F85149', borderColor: '#F8514940' }}
        disabled={!canManage}
        title={!canManage ? 'Requires host.manage' : 'Remove this host from the inventory (physical VMs on the host are untouched)'}
        onClick={() => setAsking(true)}
      >
        Remove host
      </button>
      <Modal
        open={asking}
        onClose={close}
        title={`Remove ${host.id}`}
        footer={
          <>
            <button type="button" className="btn-secondary-sm" onClick={close}>Cancel</button>
            <button
              type="button"
              className="btn-primary-sm"
              style={{ background: '#F85149', borderColor: '#F85149' }}
              disabled={!canSubmit}
              onClick={() => del.mutate()}
            >
              {del.isPending ? 'Removing…' : 'Remove host'}
            </button>
          </>
        }
      >
        <div style={{ display: 'grid', gap: 12, fontSize: 12, color: 'var(--text-body)' }}>
          <div>
            This drops <strong>{host.id}</strong> from the inventory along with every VM,
            storage pool, and enrolment cert the server has associated with it. Physical
            VMs on the hypervisor are <strong>not</strong> touched.
          </div>
          <div style={{ color: 'var(--text-faded)' }}>
            To re-enrol, mint a fresh token on the Deployment page and run
            <code> install-agent.sh</code> on the host.
          </div>
          <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text-muted)' }}>
            Type <code style={{ color: 'var(--text-strong)' }}>{host.id}</code> to confirm
            <input
              type="text"
              autoFocus
              value={confirmName}
              onChange={(e) => setConfirmName(e.target.value)}
              style={{
                padding: '6px 10px',
                fontSize: 13,
                background: 'var(--surface-2)',
                color: 'var(--text-strong)',
                border: '1px solid var(--border)',
                borderRadius: 4,
              }}
            />
          </label>
        </div>
      </Modal>
    </>
  );
}

export function HostDetail() {
  const { id } = useParams<{ id: string }>();
  const hosts = useHosts();
  const host = useMemo(() => (hosts.data ?? []).find((h) => h.id === id), [hosts.data, id]);

  if (hosts.isLoading) return <div style={{ color: 'var(--text-muted)' }}>Loading…</div>;
  if (!host) {
    return (
      <div className="panel">
        <h2>Host not found</h2>
        <p style={{ color: 'var(--text-muted)', fontSize: 12, margin: 0 }}>
          The requested hypervisor isn't in the current inventory.
        </p>
      </div>
    );
  }
  return (
    <>
      <Tabs id={host.id} />
      <TabBody host={host} />
    </>
  );
}

function Tabs({ id }: { id: string }) {
  return (
    <div style={tabBar}>
      {[
        { to: '', label: 'Summary' },
        { to: 'vms', label: 'VMs' },
        { to: 'storage', label: 'Storage' },
        { to: 'hardware', label: 'Hardware' },
      ].map((t) => (
        <NavLink
          key={t.to}
          to={t.to ? `/hosts/${id}/${t.to}` : `/hosts/${id}`}
          end
          style={({ isActive }) => ({
            ...tab,
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

function TabBody({ host }: { host: Host }) {
  const isVMs      = useMatch('/hosts/:id/vms');
  const isStorage  = useMatch('/hosts/:id/storage');
  const isHardware = useMatch('/hosts/:id/hardware');
  if (isVMs) return <VMsTab host={host} />;
  if (isStorage) return <StorageTab host={host} />;
  if (isHardware) return <HardwareTab host={host} />;
  return <SummaryTab host={host} />;
}

/* ── Summary ─────────────────────────────────────────────── */
function SummaryTab({ host }: { host: Host }) {
  const vms = useVMs();
  const storage = useStorage();

  const hostVMs = useMemo(
    () => (vms.data ?? []).filter((v) => v.host_id === host.id),
    [vms.data, host.id],
  );
  const pools = useMemo(
    () => (storage.data ?? []).filter((p) => p.host_id === host.id),
    [storage.data, host.id],
  );

  const running = hostVMs.filter((v) => v.state === 'running').length;
  const cpuPct = Math.min(host.cpu_usage || 0, 100);
  const memUsed = host.mem_total_mb - host.mem_free_mb;
  const memFreeGB = (host.mem_free_mb / 1024).toFixed(1);
  const memTotalGB = (host.mem_total_mb / 1024).toFixed(1);
  const memPct = host.mem_total_mb > 0 ? (memUsed / host.mem_total_mb) * 100 : 0;

  return (
    <div style={{ display: 'grid', gap: 16 }}>
      {/* Heading block: host + IP + meta row + ONLINE pill */}
      <div className="panel" style={{ padding: '18px 22px' }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16 }}>
          <div>
            <div style={{ fontSize: 22, fontWeight: 600, color: 'var(--text-strong)', letterSpacing: '-0.01em' }}>
              {host.id}
            </div>
            <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4, fontFamily: "'SF Mono', monospace" }}>
              {host.ip}
            </div>
          </div>
          <span
            className={`tone-pill ${host.status === 'online' ? 'ok' : 'muted'}`}
            style={{ padding: '4px 12px', borderRadius: 12 }}
          >
            {host.status}
          </span>
          {host.maintenance_mode && (
            <span
              className="tone-pill warn"
              style={{ padding: '4px 12px', borderRadius: 12 }}
              title={host.maintenance_reason || 'Host is in maintenance mode'}
            >
              maintenance
            </span>
          )}
          {host.mtls_ready && (
            <span
              className="tone-pill ok"
              style={{ padding: '4px 10px', borderRadius: 12, display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 11 }}
              title="Hypervisor has an active cluster CA certificate — migrations use qemu+tls://"
            >
              <svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
                <path d="M8 1a3.5 3.5 0 0 0-3.5 3.5V7H4a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V8a1 1 0 0 0-1-1h-.5V4.5A3.5 3.5 0 0 0 8 1zm2.5 6h-5V4.5a2.5 2.5 0 1 1 5 0V7z"/>
              </svg>
              mTLS
            </span>
          )}
          <div style={{ flex: 1 }} />
          <UpdateAgentButton host={host} />
          <MaintenanceToggle host={host} />
          <DeleteHostButton host={host} />
        </div>
        <div
          style={{
            marginTop: 14,
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 16,
          }}
        >
          <Meta label="Operating system" value={host.os || '—'} />
          <Meta label="Kernel" value={host.kernel_ver || '—'} mono />
          <Meta label="libvirt" value={host.libvirt_ver || '—'} />
          <Meta label="Uptime" value={host.uptime || '—'} />
          <Meta label="Agent port" value={`${host.agent_port || 8081}`} />
          <Meta label="Last seen" value={relTime(host.last_seen)} />
        </div>
      </div>

      {/* Four-up stat row */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12 }}>
        <BigStat
          label="CPUs"
          value={`${host.cpu_count}`}
          sub="logical cores"
          accent="#E3571E"
        />
        <BigStat
          label="Memory"
          value={`${memTotalGB} GB`}
          sub={`${memFreeGB} GB free`}
        />
        <BigStat
          label="VMs"
          value={`${hostVMs.length}`}
          sub={`${running} running`}
          accent={running > 0 ? '#3FB950' : undefined}
        />
        <BigStat
          label="Storage pools"
          value={`${pools.length}`}
          sub="libvirt pools"
        />
      </div>

      {/* Resource utilisation */}
      <div>
        <div style={sectionHeader}>Resource utilisation</div>
        <div className="panel" style={{ padding: '16px 20px' }}>
          <UtilBar label="CPU Usage" pct={cpuPct} color="#E3571E" />
          <div style={{ height: 12 }} />
          <UtilBar label="Memory Usage" pct={memPct} color="#8B5CF6" />
        </div>
      </div>

      {/* In-UI sparklines (v5.17) — glance at the last hour. */}
      <HostMetricsGraphs hostID={host.id} />
    </div>
  );
}

function HostVMRow({
  vm, latestKVMTools, lock,
}: { vm: VM; host: Host; latestKVMTools: string; lock?: LockReason }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const cmd = useVMCommand();

  const locked = !!lock;
  const lockTitle = lock === 'migrating'
    ? 'Migration in progress — actions are disabled until it finishes'
    : lock === 'rolled-back'
      ? 'Migration rolled back — destination may have partial artifacts. Investigate before acting.'
      : '';

  const canLife = hasPerm('vm.lifecycle') && !locked;
  const canSnap = hasPerm('vm.snapshot') && !features.data?.disable_snapshots && !locked;
  const canConsole = hasPerm('vm.console') && !features.data?.disable_console && !locked;

  const running = vm.state === 'running';
  const stopped = vm.state === 'stopped';
  const run = (action: Parameters<typeof cmd.mutate>[0]['action']) => () =>
    cmd.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, action });

  const openConsole = () => {
    if (!canConsole) return;
    // Same pattern as VMDetail.tsx: server-proxied console keyed by
    // session cookie. The previous direct-to-agent URL
    // (http://<hv-ip>:8081/console/<vm>) bypassed server auth + ran
    // into mixed-content blocking on https deployments, so the button
    // silently did nothing.
    const url = `/api/console/${encodeURIComponent(vm.host_id)}/${encodeURIComponent(vm.name)}`;
    window.open(url, '_blank', 'noopener,noreferrer');
  };

  const [snapOpen, setSnapOpen] = useState(false);

  const cpuPct = Math.min(vm.cpu_usage || 0, 100);

  const rowStyle: React.CSSProperties = locked
    ? { opacity: 0.45, cursor: 'not-allowed', background: 'var(--surface-2)' }
    : {};

  return (
    <>
    <tr style={rowStyle} title={lockTitle}>
      <td>
        {locked ? (
          <span style={{ color: 'var(--text-muted)', fontWeight: 500 }}>{vm.name}</span>
        ) : (
          <Link
            to={`/vms/${encodeURIComponent(vm.uuid)}`}
            style={{ color: 'var(--text-strong)', fontWeight: 500 }}
          >
            {vm.name}
          </Link>
        )}
        {lock && <LockBadge reason={lock} />}
      </td>
      <td><StatePill state={vm.state} /></td>
      <td>{vm.cpu_count}</td>
      <td>
        {vm.mem_total_mb > 0
          ? `${(vm.mem_total_mb / 1024).toFixed(1)} GB`
          : '—'}
      </td>
      <td>{vm.disk_total_gb > 0 ? `${vm.disk_total_gb.toFixed(0)} GB` : '—'}</td>
      <td>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <div style={{ width: 100, height: 5, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
            <div style={{ width: `${cpuPct.toFixed(1)}%`, height: '100%', background: cpuPct > 85 ? '#F85149' : cpuPct > 65 ? '#FFB020' : '#E3571E' }} />
          </div>
          <span style={{ fontSize: 11, color: 'var(--text-muted)', minWidth: 36 }}>{cpuPct.toFixed(1)}%</span>
        </div>
      </td>
      <td>
        <KVMToolsPill vm={vm} latest={latestKVMTools} showVersion />
      </td>
      <td>
        <div style={{ display: 'flex', gap: 4 }}>
          <IconAction title="Start" tone="primary" disabled={!stopped || !canLife} onClick={run('START')} icon="▶" />
          <IconAction title="Shutdown" tone="warn"    disabled={!running || !canLife} onClick={run('STOP')}    icon="■" />
          <IconAction title="Restart"  tone="ghost"   disabled={!running || !canLife} onClick={run('RESTART')} icon="↺" />
          <IconAction title="Take snapshot" tone="ghost" disabled={!canSnap || !running} onClick={() => setSnapOpen(true)} icon="📷" />
          <IconAction title="Console"  tone="ghost"   disabled={!running || !canConsole} onClick={openConsole} icon="🖥" />
        </div>
      </td>
    </tr>
    <CreateSnapshotModal vm={vm} open={snapOpen} onClose={() => setSnapOpen(false)} />
    </>
  );
}

function IconAction({
  title, tone, disabled, onClick, icon,
}: { title: string; tone: 'primary' | 'warn' | 'ghost'; disabled?: boolean; onClick?: () => void; icon: string }) {
  const bg =
    disabled ? 'var(--surface-3)' :
    tone === 'primary' ? '#E3571E' :
    tone === 'warn' ? '#5A3A0A' :
    'var(--surface-2)';
  const color =
    disabled ? 'var(--text-disabled)' :
    tone === 'primary' ? '#fff' :
    tone === 'warn' ? '#FFCC66' :
    'var(--text-body)';
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      disabled={disabled}
      onClick={onClick}
      style={{
        width: 28, height: 26,
        display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
        border: '1px solid var(--border)', borderRadius: 4,
        background: bg, color, fontSize: 12,
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.5 : 1,
      }}
    >
      {icon}
    </button>
  );
}

function Meta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)' }}>
        {label}
      </div>
      <div
        style={{
          marginTop: 4,
          fontSize: 12,
          color: 'var(--text-body)',
          fontFamily: mono ? "'SF Mono', monospace" : 'inherit',
          wordBreak: 'break-word',
        }}
      >
        {value}
      </div>
    </div>
  );
}

function BigStat({
  label, value, sub, accent,
}: { label: string; value: string; sub?: string; accent?: string }) {
  return (
    <div
      className="panel"
      style={{ padding: '16px 20px', borderLeft: accent ? `3px solid ${accent}` : undefined }}
    >
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)' }}>
        {label}
      </div>
      <div style={{ fontSize: 30, fontWeight: 300, color: 'var(--text-strong)', marginTop: 6 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function UtilBar({ label, pct, color }: { label: string; pct: number; color: string }) {
  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
        <span style={{ fontSize: 12, color: 'var(--text-body)' }}>{label}</span>
        <span style={{ fontSize: 12, color: 'var(--text-strong)' }}>{pct.toFixed(1)}%</span>
      </div>
      <div style={{ width: '100%', height: 8, background: 'var(--surface-3)', borderRadius: 4, overflow: 'hidden' }}>
        <div style={{ width: `${Math.min(pct, 100).toFixed(1)}%`, height: '100%', background: color, transition: 'width 0.3s' }} />
      </div>
    </>
  );
}

function relTime(iso: string): string {
  if (!iso) return '—';
  const diff = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (diff < 60) return `${Math.floor(diff)}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

const sectionHeader: React.CSSProperties = {
  fontSize: 10,
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  color: 'var(--text-faded)',
  margin: '4px 0 8px 4px',
};

/* ── VMs on this host ────────────────────────────────────── */
type VMSortKey = 'name' | 'state' | 'cpu_count' | 'mem_total_mb' | 'disk_total_gb' | 'cpu_usage';

function VMsTab({ host }: { host: Host }) {
  const vms = useVMs();
  const latest = useKVMToolsLatest();
  const migrations = useMigrations();
  const { sort, onToggle, compare } = useSort<VMSortKey>('name');
  // Same lock logic the top-level VMs page uses — source row gets the
  // "migrating" badge while in flight, destination row gets a
  // "rolled-back" badge if the migration failed after defining. Prevents
  // double-starts, accidental edits, etc.
  const locks = useMemo(() => {
    const m = new Map<string, LockReason>();
    for (const r of migrations.data ?? []) {
      if (r.status === 'running' && r.source_host && r.vm_name) {
        m.set(lockKey(r.source_host, r.vm_name), 'migrating');
      } else if (r.status === 'failed' && r.dest_host && r.vm_name && PHASES_PAST_DEFINE.has(r.phase ?? '')) {
        m.set(lockKey(r.dest_host, r.vm_name), 'rolled-back');
      }
    }
    return m;
  }, [migrations.data]);
  const filtered = useMemo(
    () => (vms.data ?? []).filter((v: VM) => v.host_id === host.id),
    [vms.data, host.id],
  );
  const list = useMemo(
    () => [...filtered].sort((a, b) => compare(a[sort.key as keyof VM], b[sort.key as keyof VM])),
    [filtered, sort, compare],
  );
  const pg = usePagination(list);
  if (filtered.length === 0) {
    return <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>No VMs on this host.</div>;
  }
  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <div className="data-table-scroll">
        <table className="data-table">
          <thead>
            <tr>
              <SortableTH colKey="name"          label="Name"      sort={sort} onToggle={onToggle} />
              <SortableTH colKey="state"         label="State"     sort={sort} onToggle={onToggle} />
              <SortableTH colKey="cpu_count"     label="CPUs"      sort={sort} onToggle={onToggle} />
              <SortableTH colKey="mem_total_mb"  label="Memory"    sort={sort} onToggle={onToggle} />
              <SortableTH colKey="disk_total_gb" label="Disk"      sort={sort} onToggle={onToggle} />
              <SortableTH colKey="cpu_usage"     label="CPU usage" sort={sort} onToggle={onToggle} style={{ width: 160 }} />
              <th>Guest tools</th>
              <th style={{ width: 180 }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {pg.items.map((v) => (
              <HostVMRow
                key={v.uuid}
                vm={v}
                host={host}
                latestKVMTools={latest.data?.version ?? ''}
                lock={locks.get(lockKey(v.host_id, v.name))}
              />
            ))}
          </tbody>
        </table>
      </div>
      <PaginationBar pagination={pg} noun="VMs" />
    </div>
  );
}

/* ── Storage pools on this host ─────────────────────────── */
type PoolSortKey = 'name' | 'pool_type' | 'state' | 'capacity_gb' | 'allocation_gb' | 'available_gb' | 'path';

function StorageTab({ host }: { host: Host }) {
  const storage = useStorage();
  const { sort, onToggle, compare } = useSort<PoolSortKey>('name');
  const filtered = useMemo(
    () => (storage.data ?? []).filter((p: StoragePool) => p.host_id === host.id),
    [storage.data, host.id],
  );
  const pools = useMemo(
    () => [...filtered].sort((a, b) => compare(a[sort.key as keyof StoragePool], b[sort.key as keyof StoragePool])),
    [filtered, sort, compare],
  );
  if (pools.length === 0) {
    return <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>No storage pools reported.</div>;
  }
  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <div className="data-table-scroll">
      <table className="data-table">
        <thead>
          <tr>
            <SortableTH colKey="name"          label="Pool"      sort={sort} onToggle={onToggle} />
            <SortableTH colKey="pool_type"     label="Type"      sort={sort} onToggle={onToggle} />
            <SortableTH colKey="state"         label="State"     sort={sort} onToggle={onToggle} />
            <SortableTH colKey="capacity_gb"   label="Capacity"  sort={sort} onToggle={onToggle} />
            <SortableTH colKey="allocation_gb" label="Used"      sort={sort} onToggle={onToggle} />
            <SortableTH colKey="available_gb"  label="Available" sort={sort} onToggle={onToggle} />
            <th style={{ width: 180 }}>Utilisation</th>
            <SortableTH colKey="path"          label="Path"      sort={sort} onToggle={onToggle} />
          </tr>
        </thead>
        <tbody>
          {pools.map((p) => {
            const pct = p.capacity_gb > 0 ? (p.allocation_gb / p.capacity_gb) * 100 : 0;
            const color = pct > 85 ? '#F85149' : pct > 65 ? '#FFB020' : '#3FB950';
            return (
              <tr key={p.name}>
                <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{p.name}</td>
                <td style={{ color: 'var(--text-muted)' }}>{p.pool_type || '—'}</td>
                <td><StatePill state={p.state === 'running' ? 'running' : 'stopped'} label={p.state} /></td>
                <td>{p.capacity_gb.toFixed(1)} GB</td>
                <td>{p.allocation_gb.toFixed(1)} GB</td>
                <td>{p.available_gb.toFixed(1)} GB</td>
                <td>
                  <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <div style={{ width: 100, height: 6, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
                      <div style={{ width: `${Math.min(pct, 100).toFixed(1)}%`, height: '100%', background: color }} />
                    </div>
                    <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>{pct.toFixed(0)}%</span>
                  </div>
                </td>
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
                  title={p.path}
                >
                  {p.path || '—'}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      </div>
    </div>
  );
}

/* ── Hardware ────────────────────────────────────────────── */
function HardwareTab({ host }: { host: Host }) {
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div className="panel">
        <h2>CPU / Memory topology</h2>
        <KV label="CPU model" value={host.cpu_model || '—'} />
        <KV label="Sockets" value={host.sockets ? `${host.sockets}` : '—'} />
        <KV label="Cores per socket" value={host.cores_per_socket ? `${host.cores_per_socket}` : '—'} />
        <KV label="Threads per core" value={host.threads_per_core ? `${host.threads_per_core}` : '—'} />
        <KV label="Total logical CPUs" value={`${host.cpu_count}`} />
        <KV label="NUMA nodes" value={host.numa_nodes ? `${host.numa_nodes}` : '—'} />
        <KV label="Memory" value={host.mem_total_mb > 0 ? `${(host.mem_total_mb / 1024).toFixed(1)} GB` : '—'} />
      </div>

      {host.gpus && host.gpus.length > 0 && (
        <div className="panel">
          <h2>GPUs</h2>
          {host.gpus.map((g) => (
            <div key={g} style={{ fontFamily: "'SF Mono', monospace", fontSize: 12, color: 'var(--text-body)', padding: '4px 0' }}>
              {g}
            </div>
          ))}
        </div>
      )}

      {host.host_pci_devices && host.host_pci_devices.length > 0 && (
        <div className="panel">
          <h2>PCI devices</h2>
          <table className="data-table">
            <thead>
              <tr>
                <th>Address</th>
                <th>Type</th>
                <th>Description</th>
                <th>IOMMU</th>
                <th>In use by</th>
              </tr>
            </thead>
            <tbody>
              {host.host_pci_devices.map((d: HostPCIDevice) => (
                <tr key={d.address}>
                  <td style={{ fontFamily: "'SF Mono', monospace", color: '#FF8A3C' }}>{d.address}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{d.type}</td>
                  <td>{d.description}</td>
                  <td style={{ textAlign: 'center' }}>{d.iommu_group}</td>
                  <td style={{ color: d.in_use_by_vm ? 'var(--text-body)' : 'var(--text-faded)' }}>
                    {d.in_use_by_vm ?? '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/* ── Shared bits ─────────────────────────────────────────── */
function KV({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', padding: '6px 0', gap: 12 }}>
      <div style={{ width: 160, fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
      <div
        style={{
          fontSize: 12,
          color: 'var(--text-body)',
          fontFamily: mono ? "'SF Mono', monospace" : 'inherit',
        }}
      >
        {value}
      </div>
    </div>
  );
}

const tabBar: React.CSSProperties = {
  display: 'flex',
  gap: 4,
  borderBottom: '1px solid var(--border)',
  marginBottom: 16,
};
const tab: React.CSSProperties = {
  padding: '8px 16px',
  fontSize: 13,
  fontWeight: 500,
  textDecoration: 'none',
  borderBottom: '2px solid transparent',
};
