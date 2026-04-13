import { useMemo } from 'react';
import { Link, NavLink, useMatch, useParams } from 'react-router-dom';

import { useHosts, useStorage, useVMs } from '@/hooks/useInventory';
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
      <HeaderRow host={host} />
      <Tabs id={host.id} />
      <TabBody host={host} />
    </>
  );
}

function HeaderRow({ host }: { host: Host }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
      <div style={{ fontSize: 18, fontWeight: 600, color: 'var(--text-strong)' }}>{host.id}</div>
      <span className={`state-pill ${host.status === 'online' ? 'running' : 'stopped'}`}>{host.status}</span>
      <div style={{ flex: 1 }} />
      <span style={{ color: 'var(--text-muted)', fontSize: 11, fontFamily: "'SF Mono', monospace" }}>
        {host.ip}:{host.agent_port || 8081}
      </span>
    </div>
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
  const cpuPct = Math.min(host.cpu_usage || 0, 100);
  const memUsed = host.mem_total_mb - host.mem_free_mb;
  const memPct = host.mem_total_mb > 0 ? (memUsed / host.mem_total_mb) * 100 : 0;
  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
        <Stat label="CPU" value={`${cpuPct.toFixed(1)}%`} sub={`${host.cpu_count} cores`} tone={toneFor(cpuPct)} />
        <Stat
          label="Memory"
          value={`${(memUsed / 1024).toFixed(1)} / ${(host.mem_total_mb / 1024).toFixed(0)} GB`}
          sub={`${memPct.toFixed(0)}% used`}
          tone={toneFor(memPct)}
        />
        <Stat label="VMs" value={`${host.vm_count}`} sub={host.status === 'online' ? 'reporting' : 'offline'} />
      </div>

      <div className="panel">
        <h2>Host details</h2>
        <KV label="ID" value={host.id} />
        <KV label="IP address" value={`${host.ip}:${host.agent_port || 8081}`} mono />
        <KV label="Operating system" value={host.os || '—'} />
        <KV label="Kernel" value={host.kernel_ver || '—'} />
        <KV label="libvirt version" value={host.libvirt_ver || '—'} />
        <KV label="Uptime" value={host.uptime || '—'} />
        <KV label="Last report" value={new Date(host.last_seen).toLocaleString()} />
      </div>
    </div>
  );
}

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
          <h2>GPUs ({host.gpus.length})</h2>
          {host.gpus.map((g) => (
            <div key={g} style={{ fontFamily: "'SF Mono', monospace", fontSize: 12, color: 'var(--text-body)', padding: '4px 0' }}>
              {g}
            </div>
          ))}
        </div>
      )}

      {host.host_pci_devices && host.host_pci_devices.length > 0 && (
        <div className="panel">
          <h2>PCI devices ({host.host_pci_devices.length})</h2>
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
function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'ok' | 'warn' | 'danger' }) {
  const color = tone === 'danger' ? '#F85149' : tone === 'warn' ? '#FFB020' : 'var(--text-strong)';
  return (
    <div className="panel" style={{ padding: '14px 18px' }}>
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)' }}>
        {label}
      </div>
      <div style={{ fontSize: 22, fontWeight: 300, color, marginTop: 6 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function toneFor(pct: number): 'ok' | 'warn' | 'danger' {
  if (pct > 85) return 'danger';
  if (pct > 65) return 'warn';
  return 'ok';
}

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
