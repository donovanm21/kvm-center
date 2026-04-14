import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';

interface AuditEvent {
  id: string;
  at: string;
  actor_id?: string;
  actor_name?: string;
  actor_ip?: string;
  action: string;
  resource?: string;
  detail?: Record<string, string>;
  success: boolean;
  error?: string;
}

interface AuditListReply {
  events: AuditEvent[];
  total: number;
  offset: number;
  limit: number;
}

// Canonical action list mirrors the constants in server/audit.go so
// the filter dropdown stays in lockstep. Ordered roughly by how often
// an operator needs to grep for them.
const ACTIONS: { value: string; label: string }[] = [
  { value: '', label: 'All actions' },
  { value: 'user.login', label: 'user.login' },
  { value: 'user.logout', label: 'user.logout' },
  { value: 'vm.action', label: 'vm.action (start/stop/restart)' },
  { value: 'vm.create', label: 'vm.create' },
  { value: 'vm.delete', label: 'vm.delete' },
  { value: 'vm.snapshot', label: 'vm.snapshot' },
  { value: 'vm.snapshot.revert', label: 'vm.snapshot.revert' },
  { value: 'vm.snapshot.delete', label: 'vm.snapshot.delete' },
  { value: 'vm.console', label: 'vm.console' },
  { value: 'vm.notes', label: 'vm.notes' },
  { value: 'migration.start', label: 'migration.start' },
  { value: 'migration.cancel', label: 'migration.cancel' },
  { value: 'host.maintenance.enter', label: 'host.maintenance.enter' },
  { value: 'host.maintenance.exit', label: 'host.maintenance.exit' },
  { value: 'tls.upload', label: 'tls.upload' },
  { value: 'tls.regenerate', label: 'tls.regenerate' },
  { value: 'db.migrate', label: 'db.migrate' },
  { value: 'features.update', label: 'features.update' },
  { value: 'user.create', label: 'user.create' },
  { value: 'user.update', label: 'user.update' },
  { value: 'user.delete', label: 'user.delete' },
  { value: 'role.create', label: 'role.create' },
  { value: 'role.update', label: 'role.update' },
  { value: 'role.delete', label: 'role.delete' },
  { value: 'idp.create', label: 'idp.create' },
  { value: 'idp.update', label: 'idp.update' },
  { value: 'idp.delete', label: 'idp.delete' },
  { value: 'kvm-tools.queue-update', label: 'kvm-tools.queue-update' },
];

const DEFAULT_LIMIT = 100;
const LIMIT_OPTIONS = [50, 100, 250, 500];

/**
 * Settings → Audit Log: browse every mutation with actor + timestamp +
 * resource + outcome. Filters: action, actor substring, resource
 * substring, outcome, date range. Default view shows the last
 * DEFAULT_LIMIT entries; operator can bump the page size or page
 * through older events via offset controls.
 */
export function AuditTab() {
  const [action, setAction] = useState('');
  const [actor, setActor] = useState('');
  const [resource, setResource] = useState('');
  const [outcome, setOutcome] = useState<'' | 'true' | 'false'>('');
  // Date range as local-wall-clock ISO strings ("YYYY-MM-DDTHH:MM"),
  // then converted to unix seconds for the server filter.
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');
  const [offset, setOffset] = useState(0);
  const [limit, setLimit] = useState(DEFAULT_LIMIT);

  const queryKey = useMemo(
    () => ['audit', action, actor, resource, outcome, since, until, offset, limit],
    [action, actor, resource, outcome, since, until, offset, limit],
  );

  const { data, isLoading, error, isFetching } = useQuery({
    queryKey,
    queryFn: () => {
      const params = new URLSearchParams();
      if (action) params.set('action', action);
      if (actor) params.set('actor', actor);
      if (resource) params.set('resource', resource);
      if (outcome) params.set('success', outcome);
      if (since) params.set('since', String(Math.floor(new Date(since).getTime() / 1000)));
      if (until) params.set('until', String(Math.floor(new Date(until).getTime() / 1000)));
      params.set('offset', String(offset));
      params.set('limit', String(limit));
      return apiGet<AuditListReply>(`/api/audit?${params.toString()}`);
    },
    staleTime: 15_000,
  });

  const resetFilters = () => {
    setAction(''); setActor(''); setResource(''); setOutcome('');
    setSince(''); setUntil(''); setOffset(0);
  };

  const events = data?.events ?? [];
  const total = data?.total ?? 0;
  const hasFilters = Boolean(action || actor || resource || outcome || since || until);

  return (
    <>
      {/* Filter toolbar */}
      <div className="panel" style={{ marginBottom: 14 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 10, marginBottom: 10 }}>
          <label style={field}>
            <span style={label}>Action</span>
            <select value={action} onChange={(e) => { setAction(e.target.value); setOffset(0); }} style={input}>
              {ACTIONS.map((a) => <option key={a.value} value={a.value}>{a.label}</option>)}
            </select>
          </label>
          <label style={field}>
            <span style={label}>Actor (exact user id)</span>
            <input type="text" value={actor} onChange={(e) => { setActor(e.target.value); setOffset(0); }} placeholder="e.g. user_ae91b4…" style={input} />
          </label>
          <label style={field}>
            <span style={label}>Resource (substring)</span>
            <input type="text" value={resource} onChange={(e) => { setResource(e.target.value); setOffset(0); }} placeholder="vm/… host/… role/…" style={input} />
          </label>
          <label style={field}>
            <span style={label}>Outcome</span>
            <select value={outcome} onChange={(e) => { setOutcome(e.target.value as '' | 'true' | 'false'); setOffset(0); }} style={input}>
              <option value="">All</option>
              <option value="true">Success</option>
              <option value="false">Failure</option>
            </select>
          </label>
          <label style={field}>
            <span style={label}>From</span>
            <input type="datetime-local" value={since} onChange={(e) => { setSince(e.target.value); setOffset(0); }} style={input} />
          </label>
          <label style={field}>
            <span style={label}>To</span>
            <input type="datetime-local" value={until} onChange={(e) => { setUntil(e.target.value); setOffset(0); }} style={input} />
          </label>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <button type="button" className="btn-secondary-sm" onClick={resetFilters} disabled={!hasFilters}>Reset</button>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--text-muted)' }}>
            Page size
            <select value={limit} onChange={(e) => { setLimit(parseInt(e.target.value, 10)); setOffset(0); }} style={{ ...input, width: 90 }}>
              {LIMIT_OPTIONS.map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </label>
          <div style={{ flex: 1 }} />
          <span style={{ fontSize: 11, color: 'var(--text-faded)' }}>
            {total > 0 ? `${offset + 1}–${Math.min(offset + limit, total)} of ${total} matching events` : '—'}
          </span>
          <button type="button" className="btn-secondary-sm" onClick={() => setOffset(Math.max(0, offset - limit))} disabled={offset === 0 || isFetching}>Newer</button>
          <button type="button" className="btn-secondary-sm" onClick={() => setOffset(offset + limit)} disabled={offset + limit >= total || isFetching}>Older</button>
        </div>
      </div>

      {/* Result table */}
      {isLoading && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading audit log…</div>}
      {error && <div className="tone-panel danger" style={{ padding: 12, fontSize: 12 }}>Failed to load: {(error as Error).message}</div>}
      {!isLoading && !error && (
        <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
          {events.length === 0 ? (
            <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: 'var(--text-muted)' }}>
              {hasFilters ? 'No audit events match the current filters.' : 'Audit log is empty — new events will show up here.'}
            </div>
          ) : (
            <table className="data-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Actor</th>
                  <th>Action</th>
                  <th>Resource</th>
                  <th>Detail</th>
                  <th>Outcome</th>
                </tr>
              </thead>
              <tbody>
                {events.map((ev) => (
                  <tr key={ev.id}>
                    <td style={{ fontSize: 11, color: 'var(--text-faded)', fontFamily: "'SF Mono', monospace" }} title={ev.at}>
                      {new Date(ev.at).toLocaleString()}
                    </td>
                    <td>
                      <div style={{ fontSize: 12, color: 'var(--text-body)' }}>{ev.actor_name || '—'}</div>
                      {ev.actor_ip && <div style={{ fontSize: 10, color: 'var(--text-faded)', fontFamily: "'SF Mono', monospace" }}>{ev.actor_ip}</div>}
                    </td>
                    <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 12, color: 'var(--text-strong)' }}>{ev.action}</td>
                    <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11, color: 'var(--text-muted)' }}>{ev.resource || '—'}</td>
                    <td style={{ fontSize: 11, color: 'var(--text-muted)', maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={detailTitle(ev.detail)}>
                      {formatDetail(ev.detail)}
                    </td>
                    <td>
                      <span className={`tone-pill ${ev.success ? 'ok' : 'danger'}`}>{ev.success ? 'success' : 'failed'}</span>
                      {!ev.success && ev.error && <div style={{ fontSize: 10, color: 'var(--text-faded)', marginTop: 2, maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={ev.error}>{ev.error}</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </>
  );
}

function formatDetail(d?: Record<string, string>): string {
  if (!d) return '';
  return Object.entries(d).map(([k, v]) => `${k}=${v}`).join(' ');
}
function detailTitle(d?: Record<string, string>): string {
  if (!d) return '';
  return Object.entries(d).map(([k, v]) => `${k}=${v}`).join('\n');
}

const field: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 4 };
const label: React.CSSProperties = { fontSize: 11, color: 'var(--text-faded)', textTransform: 'uppercase', letterSpacing: '0.08em' };
const input: React.CSSProperties = {
  height: 32, padding: '0 10px',
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontSize: 12, fontFamily: 'inherit',
};
