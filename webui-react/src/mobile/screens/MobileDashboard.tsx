import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import type { Host, VM, MigrationRecord } from '@/types';

// Read-only overview: stat tiles + most-recent activity. The same
// queries the desktop Dashboard uses, so the React Query cache is
// shared if the operator flips between mobile and desktop.
export function MobileDashboard() {
  const hosts = useQuery({ queryKey: qk.hosts(), queryFn: () => apiGet<Host[]>('/api/hosts') });
  const vms = useQuery({ queryKey: qk.vms(), queryFn: () => apiGet<VM[]>('/api/vms') });
  const migrations = useQuery({ queryKey: qk.migrations(), queryFn: () => apiGet<MigrationRecord[]>('/api/migrations') });

  const hostList = hosts.data ?? [];
  const vmList = vms.data ?? [];
  const migList = migrations.data ?? [];

  const onlineHosts = hostList.filter((h) => h.status === 'online').length;
  const runningVMs = vmList.filter((v) => v.state === 'running').length;
  const activeMigs = migList.filter((m) => m.status === 'running').length;
  const recentMigs = [...migList]
    .sort((a, b) => (b.started_at || 0) - (a.started_at || 0))
    .slice(0, 5);

  return (
    <div className="m-screen" data-testid="mobile-dashboard">
      <div className="m-screen-h">
        <h1 className="m-h1">Overview</h1>
        <p className="m-sub">Read-only fleet snapshot.</p>
      </div>

      <div className="m-stats">
        <Stat label="Hosts online" value={`${onlineHosts}/${hostList.length}`} foot={`${hostList.length - onlineHosts} offline`} />
        <Stat label="VMs running" value={`${runningVMs}/${vmList.length}`} foot={`${vmList.length - runningVMs} stopped`} />
        <Stat label="Active migrations" value={String(activeMigs)} foot={activeMigs ? 'in progress' : 'idle'} />
        <Stat label="Total VMs" value={String(vmList.length)} foot="across cluster" />
      </div>

      <div className="m-section">
        <div className="m-section-h">
          <h2>Recent migrations</h2>
          {recentMigs.length > 0 && <Link to="/m/activity">View all</Link>}
        </div>
        {migrations.isLoading && <div className="m-placeholder">Loading…</div>}
        {migrations.error && <div className="m-error">Failed to load migrations.</div>}
        {!migrations.isLoading && recentMigs.length === 0 && (
          <div className="m-placeholder">No migrations yet.</div>
        )}
        {recentMigs.map((m) => (
          <div key={m.id} className="m-card">
            <div className="m-card-row">
              <div style={{ minWidth: 0 }}>
                <div className="m-card-title" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m.vm_name}</div>
                <div className="m-card-meta">{m.source_host} → {m.dest_host}</div>
              </div>
              <MigStatePill status={m.status} />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function Stat({ label, value, foot }: { label: string; value: string; foot?: string }) {
  return (
    <div className="m-stat">
      <div className="m-stat-label">{label}</div>
      <div className="m-stat-value">{value}</div>
      {foot && <div className="m-stat-foot">{foot}</div>}
    </div>
  );
}

function MigStatePill({ status }: { status: string }) {
  const tone =
    status === 'complete' ? 'ok'
    : status === 'failed' ? 'danger'
    : status === 'running' ? 'warn'
    : 'muted';
  return <span className={`m-pill ${tone}`}>{status}</span>;
}
