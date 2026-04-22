import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';

import { Pagination } from '@/components/Pagination';
import { useHosts, useStorage, useVMs } from '@/hooks/useInventory';
import type { Host, StoragePool, VM } from '@/types';

const HOSTS_PER_PAGE = 10;

export function Dashboard() {
  const hosts = useHosts();
  const vms = useVMs();
  const storage = useStorage();
  const [page, setPage] = useState(1);

  // Split the raw lists once at the top and hand out the derived stats
  // to each card — avoids repeated .filter() per render.
  // Sort by host id so the Dashboard order is stable across telemetry
  // ticks; the raw server list can arrive in any order.
  const hostList = useMemo(
    () => [...(hosts.data ?? [])].sort((a, b) => a.id.localeCompare(b.id)),
    [hosts.data],
  );
  const totalPages = Math.max(1, Math.ceil(hostList.length / HOSTS_PER_PAGE));
  const clampedPage = Math.min(page, totalPages);
  const pagedHosts = hostList.slice(
    (clampedPage - 1) * HOSTS_PER_PAGE,
    clampedPage * HOSTS_PER_PAGE,
  );
  const onlineHosts = hostList.filter((h) => h.status === 'online');
  const offlineHosts = hostList.filter((h) => h.status === 'offline');
  const totalVMs = vms.data?.length ?? 0;
  const running = (vms.data ?? []).filter((v) => v.state === 'running').length;
  const stopped = totalVMs - running;
  const avgCPU =
    onlineHosts.length > 0
      ? onlineHosts.reduce((s, h) => s + (h.cpu_usage || 0), 0) / onlineHosts.length
      : 0;

  const vmsByHost = useMemo(() => {
    const m: Record<string, VM[]> = {};
    for (const v of vms.data ?? []) {
      (m[v.host_id] ??= []).push(v);
    }
    return m;
  }, [vms.data]);

  const storageByHost = useMemo(() => {
    const m: Record<string, StoragePool[]> = {};
    for (const p of storage.data ?? []) {
      (m[p.host_id] ??= []).push(p);
    }
    return m;
  }, [storage.data]);

  return (
    <>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 12 }}>
        <Summary
          label="Hypervisors"
          value={hostList.length}
          sub={`${onlineHosts.length} online · ${offlineHosts.length} offline`}
        />
        <Summary
          label="Total VMs"
          value={totalVMs}
          sub={`across all hosts`}
        />
        <Summary
          label="Running"
          value={running}
          sub={`${stopped} stopped`}
        />
        <Summary
          label="Avg CPU"
          value={`${avgCPU.toFixed(1)}%`}
          sub={`${onlineHosts.length} online host${onlineHosts.length === 1 ? '' : 's'}`}
        />
      </div>

      <div style={{ marginTop: 18 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}>
          <h2 style={{ margin: 0, fontSize: 14, fontWeight: 600, color: 'var(--text-strong)' }}>Hypervisors</h2>
          <span style={{ fontSize: 11, color: 'var(--text-faded)' }}>Click a hypervisor to manage</span>
        </div>

        {hosts.isLoading && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading…</div>}
        {hostList.length === 0 && !hosts.isLoading && (
          <div className="panel" style={{ color: 'var(--text-muted)', fontSize: 12 }}>
            No hypervisors have reported yet — install an agent on a hypervisor and point it at this server.
          </div>
        )}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {pagedHosts.map((h) => (
            <HostCard
              key={h.id}
              host={h}
              vms={vmsByHost[h.id] ?? []}
              pools={storageByHost[h.id] ?? []}
            />
          ))}
        </div>
        {totalPages > 1 && (
          <Pagination
            page={clampedPage}
            totalPages={totalPages}
            onChange={setPage}
            totalItems={hostList.length}
            perPage={HOSTS_PER_PAGE}
            noun="hypervisors"
          />
        )}
      </div>
    </>
  );
}

function Summary({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <div className="panel" style={{ padding: '14px 18px' }}>
      <div style={{ fontSize: 10, textTransform: 'uppercase', color: 'var(--text-faded)', letterSpacing: '0.08em' }}>
        {label}
      </div>
      <div style={{ fontSize: 26, fontWeight: 300, color: 'var(--text-strong)', marginTop: 6 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function HostCard({ host, vms, pools }: { host: Host; vms: VM[]; pools: StoragePool[] }) {
  const runningVMs = vms.filter((v) => v.state === 'running').length;
  const stoppedVMs = vms.length - runningVMs;
  const cpuPct = Math.min(host.cpu_usage || 0, 100);
  const memUsed = host.mem_total_mb - host.mem_free_mb;
  const memPct = host.mem_total_mb > 0 ? (memUsed / host.mem_total_mb) * 100 : 0;
  const ramGB = (host.mem_total_mb / 1024).toFixed(1);
  return (
    <div className="panel" style={cardStyle}>
      <div className="host-card-grid" style={{ display: 'grid', gridTemplateColumns: '72px 1fr auto', gap: 16, alignItems: 'start' }}>
        <div style={hostIconStyle} aria-hidden>
          <HostChipIcon />
        </div>
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <Link to={`/hosts/${encodeURIComponent(host.id)}`} style={hostLink}>{host.id}</Link>
            <StatusPill online={host.status === 'online'} />
          </div>
          <div style={metaRow}>
            <span><RelTime iso={host.last_seen} /></span>
            <span>libvirt {host.libvirt_ver || '—'}</span>
            <span>{host.ip}</span>
            {host.uptime && <span>Up {host.uptime}</span>}
          </div>
          <div style={{ ...metaRow, marginTop: 6 }}>
            <span>{vms.length} VMs</span>
            <span style={{ color: '#3FB950' }}>{runningVMs} running</span>
            <span>{stoppedVMs} stopped</span>
            <span>{pools.length} storage pool{pools.length === 1 ? '' : 's'}</span>
            <span>◎ {host.cpu_count} CPU</span>
            <span>{ramGB} GB RAM</span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginTop: 10 }}>
            <MetricBar label="CPU" pct={cpuPct} />
            <MetricBar label="Memory" pct={memPct} />
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 6 }}>
          <span className={`state-pill ${host.status === 'online' ? 'running' : 'stopped'}`}>
            {host.status === 'online' ? 'connected' : 'disconnected'}
          </span>
          <Link to={`/hosts/${encodeURIComponent(host.id)}`} style={manageLink}>
            Manage →
          </Link>
        </div>
      </div>
    </div>
  );
}

function StatusPill({ online }: { online: boolean }) {
  return (
    <span className={`tone-pill ${online ? 'ok' : 'muted'}`} style={{ padding: '1px 8px', borderRadius: 3 }}>
      {online ? 'UP' : 'DOWN'}
    </span>
  );
}

function MetricBar({ label, pct }: { label: string; pct: number }) {
  const color = pct > 85 ? '#F85149' : pct > 65 ? '#FFB020' : '#3FB950';
  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--text-muted)', marginBottom: 4 }}>
        <span>{label}</span>
        <span>{pct.toFixed(1)}%</span>
      </div>
      <div style={{ height: 6, background: 'var(--surface-3)', borderRadius: 3, overflow: 'hidden' }}>
        <div style={{ width: `${Math.min(pct, 100).toFixed(1)}%`, height: '100%', background: color, transition: 'width 0.3s' }} />
      </div>
    </div>
  );
}

// RelTime prints "5m ago" / "22d 12h 5m ago" from the host's last_seen
// ISO string. The backend already ticks `last_seen` on every telemetry
// burst, so this ends up matching the agent's report cadence almost
// exactly — reassuring when you're watching a new hypervisor come up.
function RelTime({ iso }: { iso: string }) {
  if (!iso) return <>—</>;
  const diff = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  let label: string;
  if (diff < 60) label = `${Math.floor(diff)}s ago`;
  else if (diff < 3600) label = `${Math.floor(diff / 60)}m ago`;
  else if (diff < 86400) label = `${Math.floor(diff / 3600)}h ago`;
  else label = `${Math.floor(diff / 86400)}d ago`;
  return <>{label}</>;
}

function HostChipIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="#FF8A3C" strokeWidth="1.5">
      <rect x="4" y="4" width="16" height="16" rx="2" />
      <rect x="8" y="8" width="8" height="8" rx="1" fill="#E3571E" fillOpacity="0.25" />
      <path d="M2 8h2M2 12h2M2 16h2M20 8h2M20 12h2M20 16h2M8 2v2M12 2v2M16 2v2M8 20v2M12 20v2M16 20v2" />
    </svg>
  );
}

const cardStyle: React.CSSProperties = { padding: '16px 18px' };
// Big accent-tinted square behind the host-card emoji; uses the same
// accent colour-pair as the .tone-icon-tile.accent class scaled up.
const hostIconStyle: React.CSSProperties = {
  width: 72,
  height: 72,
  background: 'var(--accent-subtle, #2D1810)',
  color: 'var(--accent)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
};
const hostLink: React.CSSProperties = {
  color: 'var(--text-strong)',
  fontWeight: 600,
  fontSize: 15,
  textDecoration: 'none',
};
const metaRow: React.CSSProperties = {
  display: 'flex',
  flexWrap: 'wrap',
  gap: 14,
  fontSize: 11,
  color: 'var(--text-muted)',
  marginTop: 4,
};
const manageLink: React.CSSProperties = {
  color: 'var(--accent)',
  fontSize: 12,
  fontWeight: 500,
  textDecoration: 'none',
};
