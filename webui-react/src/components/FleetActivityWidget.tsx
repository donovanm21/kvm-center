import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';

// Dashboard fleet-activity widget (v5.48.1 PR-3f).
//
// Three sub-cards:
//   1. tasks-in-last-24h sparkline (24 hourly buckets);
//   2. currently-running count;
//   3. average duration per action (top 5 by count).
//
// Reads /api/tasks/stats. Refreshes every 30 s and on the same SSE
// `command` event that the Recent Tasks pane consumes.

interface ActionStat {
  action: string;
  count: number;
  avg_ms: number;
}

interface TasksStats {
  since: number;
  running: number;
  by_outcome: Record<string, number>;
  by_action: ActionStat[] | null;
  hourly: Record<string, number>;
  now: number;
}

function fmtAvg(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const m = Math.floor(ms / 60_000);
  const sec = Math.floor((ms % 60_000) / 1000);
  return `${m}m ${sec}s`;
}

export function FleetActivityWidget() {
  const { data, isLoading, isError } = useQuery<TasksStats>({
    queryKey: [...qk.tasks(), 'stats'] as const,
    queryFn: () => apiGet<TasksStats>('/api/tasks/stats'),
    refetchInterval: 30_000,
  });

  const sparkline = useMemo(() => {
    if (!data?.hourly) return null;
    // Build a 24-bucket array ending at the current hour. Missing
    // buckets get 0 so the sparkline stays anchored to "now".
    const now = data.now ?? Math.floor(Date.now() / 1000);
    const startHour = Math.floor(now / 3600) * 3600 - 23 * 3600;
    const buckets: number[] = [];
    for (let i = 0; i < 24; i++) {
      const t = startHour + i * 3600;
      buckets.push(data.hourly[String(t)] ?? 0);
    }
    return buckets;
  }, [data]);

  if (isLoading) {
    return (
      <div className="panel fleet-activity-widget">
        <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading fleet activity…</span>
      </div>
    );
  }
  if (isError || !data) return null;

  const totalLast24 = sparkline?.reduce((a, b) => a + b, 0) ?? 0;
  if (totalLast24 === 0 && data.running === 0) {
    return (
      <div className="panel fleet-activity-widget">
        <h3 style={{ margin: '0 0 6px 0', fontSize: 13, fontWeight: 600 }}>Fleet activity</h3>
        <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>No activity yet — kick off a VM action to populate this widget.</span>
      </div>
    );
  }

  const topActions = (data.by_action ?? [])
    .slice()
    .sort((a, b) => b.count - a.count)
    .slice(0, 5);

  const sparkMax = sparkline ? Math.max(1, ...sparkline) : 1;

  return (
    <div className="panel fleet-activity-widget">
      <header style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 8 }}>
        <h3 style={{ margin: 0, fontSize: 13, fontWeight: 600 }}>Fleet activity</h3>
        <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>last 24 h</span>
      </header>
      <div className="fleet-activity-grid">
        <div className="fleet-activity-stat">
          <div className="fleet-activity-stat-label">Running now</div>
          <div className="fleet-activity-stat-value" style={{ color: data.running > 0 ? 'var(--accent, #2a7df9)' : undefined }}>
            {data.running}
          </div>
        </div>
        <div className="fleet-activity-stat">
          <div className="fleet-activity-stat-label">Tasks (24 h)</div>
          <div className="fleet-activity-stat-value">{totalLast24}</div>
          {sparkline && (
            <svg
              className="fleet-activity-sparkline"
              viewBox={`0 0 ${sparkline.length * 4} 24`}
              preserveAspectRatio="none"
              aria-hidden="true"
            >
              {sparkline.map((v, i) => {
                const h = Math.max(1, (v / sparkMax) * 22);
                return <rect key={i} x={i * 4} y={24 - h} width={3} height={h} fill="currentColor" />;
              })}
            </svg>
          )}
        </div>
        <div className="fleet-activity-stat">
          <div className="fleet-activity-stat-label">Failed (24 h)</div>
          <div
            className="fleet-activity-stat-value"
            style={{ color: (data.by_outcome?.failed ?? 0) > 0 ? 'var(--err, #f85149)' : undefined }}
          >
            {data.by_outcome?.failed ?? 0}
          </div>
        </div>
      </div>
      {topActions.length > 0 && (
        <table className="fleet-activity-actions">
          <thead>
            <tr>
              <th>Action</th>
              <th>Count</th>
              <th>Avg duration</th>
            </tr>
          </thead>
          <tbody>
            {topActions.map((a) => (
              <tr key={a.action}>
                <td>{a.action}</td>
                <td>{a.count}</td>
                <td>{a.avg_ms > 0 ? fmtAvg(a.avg_ms) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
