import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';

interface AuditEvent {
  id: string;
  at: string;
  actor_name?: string;
  actor_ip?: string;
  action: string;
  resource?: string;
  success: boolean;
  error?: string;
}
interface AuditListReply {
  events: AuditEvent[];
  total: number;
}

// Mobile activity feed: latest 50 audit events, no filters. The
// desktop AuditTab is the place for filtering / drilling — here we
// just want a glance-at-it stream.
export function MobileActivity() {
  const q = useQuery({
    queryKey: ['m-audit-recent'],
    queryFn: () => apiGet<AuditListReply>('/api/audit?offset=0&limit=50'),
    staleTime: 15_000,
  });
  const events = q.data?.events ?? [];

  return (
    <div data-testid="mobile-activity">
      <h1 className="m-h1">Activity</h1>
      <p className="m-sub">Most recent {events.length} of {q.data?.total ?? 0} events.</p>

      {q.isLoading && <div className="m-placeholder">Loading…</div>}
      {q.error && <div className="m-error">Failed to load audit log.</div>}
      {!q.isLoading && events.length === 0 && (
        <div className="m-placeholder">No audit events yet.</div>
      )}

      {events.map((e) => (
        <div key={e.id} className="m-card">
          <div className="m-card-row">
            <div style={{ minWidth: 0 }}>
              <div className="m-card-title" style={{ fontSize: 14, fontFamily: 'SF Mono, monospace' }}>{e.action}</div>
              <div className="m-card-meta">
                {(e.actor_name || 'system')}{e.resource ? ` · ${e.resource}` : ''}
              </div>
              <div className="m-card-meta" style={{ marginTop: 2 }}>{formatWhen(e.at)}</div>
            </div>
            <span className={`m-pill ${e.success ? 'ok' : 'danger'}`}>{e.success ? 'ok' : 'fail'}</span>
          </div>
          {!e.success && e.error && (
            <div style={{ marginTop: 8, fontSize: 12, color: '#F85149' }}>{e.error}</div>
          )}
        </div>
      ))}
    </div>
  );
}

function formatWhen(iso: string): string {
  try {
    const d = new Date(iso);
    const now = Date.now();
    const diff = Math.round((now - d.getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.round(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.round(diff / 3600)}h ago`;
    return d.toLocaleString();
  } catch {
    return iso;
  }
}
