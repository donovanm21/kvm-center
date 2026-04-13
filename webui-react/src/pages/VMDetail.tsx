import { useMemo } from 'react';
import { NavLink, Outlet, useMatch, useParams } from 'react-router-dom';

import { ActionsMenu, type ActionGroup } from '@/components/ActionsMenu';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useVMs } from '@/hooks/useInventory';
import { useVMCommand, type VMAction } from '@/hooks/useVMCommand';
import type { VM, Host } from '@/types';

export function VMDetail() {
  const { uuid } = useParams<{ uuid: string }>();
  const vms = useVMs();
  const hosts = useHosts();

  const vm = useMemo(() => (vms.data ?? []).find((v) => v.uuid === uuid), [vms.data, uuid]);
  const host = useMemo(() => (hosts.data ?? []).find((h) => h.id === vm?.host_id), [hosts.data, vm]);

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
      <DetailHeader vm={vm} />
      <Tabs uuid={vm.uuid} />
      <TabBody vm={vm} host={host} />
    </>
  );
}

function DetailHeader({ vm }: { vm: VM }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const cmd = useVMCommand();

  // Evaluate each action once so the menu render stays terse. Each gate
  // combines role-based permission with the admin-controlled feature flag.
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
        { label: '▶ Start', enabled: stopped && life.ok, tooltip: life.reason, onSelect: run('START') },
        { label: '■ Shutdown', enabled: running && life.ok, tooltip: life.reason, onSelect: run('STOP') },
        { label: '↺ Restart', enabled: running && life.ok, tooltip: life.reason, onSelect: run('RESTART') },
        { label: '⚡ Force Off', danger: true, enabled: running && life.ok, tooltip: life.reason, onSelect: run('FORCESTOP') },
      ],
    },
    {
      items: [
        { label: '📷 Snapshot', enabled: snap.ok, tooltip: snap.reason },
        { label: '↗ Migrate', enabled: mig.ok, tooltip: mig.reason },
      ],
    },
    {
      items: [
        { label: '🗑 Delete VM', danger: true, enabled: del.ok, tooltip: del.reason },
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
  const isMonitor   = useMatch('/vms/:uuid/monitor');
  const isHardware  = useMatch('/vms/:uuid/hardware');
  const isNetworks  = useMatch('/vms/:uuid/networks');
  const isSnapshots = useMatch('/vms/:uuid/snapshots');
  if (isMonitor)   return <ComingSoon tab="Monitor" />;
  if (isHardware)  return <ComingSoon tab="Hardware" />;
  if (isNetworks)  return <ComingSoon tab="Networks" />;
  if (isSnapshots) return <ComingSoon tab="Snapshots" />;
  // Default: Summary.
  return <Summary vm={vm} host={host} />;
}

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
        <KV
          label="CPU"
          value={`${vm.cpu_count} vCPU · ${vm.cpu_usage.toFixed(1)}% usage`}
        />
        <KV
          label="Memory"
          value={
            vm.mem_total_mb > 0
              ? `${(vm.mem_usage_mb / 1024).toFixed(1)} / ${(vm.mem_total_mb / 1024).toFixed(0)} GB`
              : '—'
          }
        />
        <KV
          label="Disk"
          value={
            vm.disk_total_gb > 0
              ? `${vm.disk_usage_gb.toFixed(1)} / ${vm.disk_total_gb.toFixed(0)} GB (${vm.disk_type ?? 'qcow2'})`
              : '—'
          }
        />
        <KV label="Guest tools" value={vm.guest_agent ? (vm.guest_source ?? 'present') : 'not installed'} />
      </div>
    </div>
  );
}

function KV({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', padding: '6px 0', gap: 12 }}>
      <div style={{ width: 120, fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
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

function ComingSoon({ tab }: { tab: string }) {
  return (
    <div className="panel">
      <h2>{tab}</h2>
      <p style={{ color: 'var(--text-muted)', fontSize: 12, margin: 0 }}>
        Detail panel port is scheduled for the next v4 commit — the v3 UI still renders this tab at{' '}
        <a href="/" style={{ color: 'var(--accent)' }}>/</a>.
      </p>
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

// Kept out of Outlet so the detail route tree stays flat — tabs live in
// the same parent component which already has vm/host in hand.
export { Outlet as _KeepForRouter };

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
