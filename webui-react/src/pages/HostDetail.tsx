import { useMemo } from 'react';
import { Link, NavLink, useMatch, useParams } from 'react-router-dom';

import { useAuth } from '@/hooks/useAuth';
import { useFeatures, useHosts, useStorage, useVMs } from '@/hooks/useInventory';
import { useVMCommand } from '@/hooks/useVMCommand';
import type { Host, HostPCIDevice, StoragePool, VM } from '@/types';

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
            style={{
              padding: '4px 12px',
              borderRadius: 12,
              background: host.status === 'online' ? '#1E3D23' : 'var(--surface-3)',
              color: host.status === 'online' ? '#3FB950' : 'var(--text-muted)',
              fontSize: 11,
              fontWeight: 700,
              letterSpacing: '0.04em',
              textTransform: 'uppercase',
            }}
          >
            {host.status}
          </span>
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

      {/* Virtual machines on this host */}
      <div>
        <div style={sectionHeader}>Virtual machines</div>
        <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
          {hostVMs.length === 0 ? (
            <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-muted)', fontSize: 12 }}>
              No VMs reported on this host yet.
            </div>
          ) : (
            <table className="data-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>State</th>
                  <th>CPUs</th>
                  <th>Memory</th>
                  <th>Disk</th>
                  <th style={{ width: 180 }}>CPU usage</th>
                  <th style={{ width: 180 }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {hostVMs.map((v) => (
                  <HostVMRow key={v.uuid} vm={v} host={host} />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function HostVMRow({ vm, host }: { vm: VM; host: Host }) {
  const { hasPerm } = useAuth();
  const features = useFeatures();
  const cmd = useVMCommand();

  const canLife = hasPerm('vm.lifecycle');
  const canSnap = hasPerm('vm.snapshot') && !features.data?.disable_snapshots;
  const canConsole = hasPerm('vm.console') && !features.data?.disable_console;

  const running = vm.state === 'running';
  const stopped = vm.state === 'stopped';
  const run = (action: Parameters<typeof cmd.mutate>[0]['action']) => () =>
    cmd.mutate({ host_id: vm.host_id, vm_uuid: vm.uuid, vm_name: vm.name, action });

  const openConsole = () => {
    if (!canConsole || !host.ip) return;
    const url = `http://${host.ip}:${host.agent_port || 8081}/console/${encodeURIComponent(vm.name)}`;
    window.open(url, '_blank', 'noopener,noreferrer');
  };

  const cpuPct = Math.min(vm.cpu_usage || 0, 100);

  return (
    <tr>
      <td>
        <Link
          to={`/vms/${encodeURIComponent(vm.uuid)}`}
          style={{ color: 'var(--text-strong)', fontWeight: 500 }}
        >
          {vm.name}
        </Link>
      </td>
      <td><span className={`state-pill ${vm.state}`}>{vm.state}</span></td>
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
        <div style={{ display: 'flex', gap: 4 }}>
          <IconAction title="Start" tone="primary" disabled={!stopped || !canLife} onClick={run('START')} icon="▶" />
          <IconAction title="Shutdown" tone="warn"    disabled={!running || !canLife} onClick={run('STOP')}    icon="■" />
          <IconAction title="Restart"  tone="ghost"   disabled={!running || !canLife} onClick={run('RESTART')} icon="↺" />
          <IconAction title="Snapshot" tone="ghost"   disabled={!canSnap} onClick={() => {/* modal lives in VM detail */}} icon="📷" />
          <IconAction title="Console"  tone="ghost"   disabled={!running || !canConsole} onClick={openConsole} icon="🖥" />
        </div>
      </td>
    </tr>
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
function VMsTab({ host }: { host: Host }) {
  const vms = useVMs();
  const list = useMemo(
    () => (vms.data ?? []).filter((v: VM) => v.host_id === host.id),
    [vms.data, host.id],
  );
  if (list.length === 0) {
    return <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>No VMs on this host.</div>;
  }
  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <table className="data-table">
        <thead><tr><th>Name</th><th>State</th><th>CPUs</th><th>Memory</th><th>Disk</th><th>Guest tools</th></tr></thead>
        <tbody>
          {list.map((v) => (
            <tr key={v.uuid}>
              <td>
                <Link to={`/vms/${encodeURIComponent(v.uuid)}`} style={{ color: 'var(--text-strong)', fontWeight: 500 }}>
                  {v.name}
                </Link>
              </td>
              <td><span className={`state-pill ${v.state}`}>{v.state}</span></td>
              <td>{v.cpu_count}</td>
              <td>
                {v.mem_total_mb > 0
                  ? `${(v.mem_usage_mb / 1024).toFixed(1)} / ${(v.mem_total_mb / 1024).toFixed(0)} GB`
                  : '—'}
              </td>
              <td>{v.disk_total_gb > 0 ? `${v.disk_usage_gb.toFixed(1)} / ${v.disk_total_gb.toFixed(0)} GB` : '—'}</td>
              <td style={{ color: v.guest_agent ? '#3FB950' : 'var(--text-faded)' }}>
                {v.guest_agent ? (v.guest_source ?? 'present') : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/* ── Storage pools on this host ─────────────────────────── */
function StorageTab({ host }: { host: Host }) {
  const storage = useStorage();
  const pools = useMemo(
    () => (storage.data ?? []).filter((p: StoragePool) => p.host_id === host.id),
    [storage.data, host.id],
  );
  if (pools.length === 0) {
    return <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>No storage pools reported.</div>;
  }
  return (
    <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
      <table className="data-table">
        <thead>
          <tr>
            <th>Pool</th><th>Type</th><th>State</th><th>Capacity</th><th>Used</th><th>Available</th>
            <th style={{ width: 180 }}>Utilisation</th><th>Path</th>
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
                <td><span className={`state-pill ${p.state === 'running' ? 'running' : 'stopped'}`}>{p.state}</span></td>
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
