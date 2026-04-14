import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';

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
  { value: 'snapshot.policy.create', label: 'snapshot.policy.create' },
  { value: 'snapshot.policy.update', label: 'snapshot.policy.update' },
  { value: 'snapshot.policy.delete', label: 'snapshot.policy.delete' },
  { value: 'snapshot.policy.run', label: 'snapshot.policy.run' },
  { value: 'audit.shipping.update', label: 'audit.shipping.update' },
  { value: 'notifications.update', label: 'notifications.update' },
  { value: 'notifications.test', label: 'notifications.test' },
  { value: 'token.create', label: 'token.create' },
  { value: 'token.revoke', label: 'token.revoke' },
  { value: 'ca.enroll', label: 'ca.enroll' },
  { value: 'ca.sign', label: 'ca.sign' },
  { value: 'ca.revoke', label: 'ca.revoke' },
  { value: 'migration.grant.issue', label: 'migration.grant.issue' },
  { value: 'migration.grant.accept', label: 'migration.grant.accept' },
  { value: 'migration.grant.reject', label: 'migration.grant.reject' },
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
interface AuditShippingCfg {
  enabled: boolean;
  type?: 'syslog' | 'http';
  endpoint?: string;
  bearer_token?: string;
  basic_auth?: string;
  syslog_facility?: number;
  syslog_app_name?: string;
  batch_size?: number;
  flush_seconds?: number;
  last_ship_at?: string;
  last_ship_error?: string;
  last_ship_count?: number;
}

/**
 * ShippingPanel sits above the audit event list. Configures where the
 * server-side shipper sends new events. Secrets (bearer / basic) are
 * never returned by the API — the form shows "(configured)" as a
 * placeholder and blanking either field leaves the stored value
 * unchanged. Explicit "Test" button fires a single synthetic event
 * at the endpoint so operators can sanity-check wiring.
 */
function ShippingPanel() {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const canManage = hasPerm('user.manage');

  const cfg = useQuery({
    queryKey: ['audit-shipping'],
    queryFn: () => apiGet<AuditShippingCfg>('/api/settings/audit-shipping'),
    staleTime: 30_000,
  });

  const [form, setForm] = useState<AuditShippingCfg | null>(null);
  useEffect(() => { if (cfg.data) setForm(cfg.data); }, [cfg.data]);

  const save = useMutation({
    mutationFn: (f: AuditShippingCfg) =>
      fetch('/api/settings/audit-shipping', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(f),
      }).then(async (r) => {
        if (!r.ok) throw new Error(await r.text());
        return r.json();
      }),
    onSuccess: () => {
      toast('success', 'Shipping config saved');
      qc.invalidateQueries({ queryKey: ['audit-shipping'] });
    },
    onError: (e) => toast('error', `Save failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const test = useMutation({
    mutationFn: () => apiPost('/api/settings/audit-shipping/test'),
    onSuccess: () => toast('success', 'Test event shipped — check your aggregator'),
    onError: (e) => toast('error', `Test failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  if (!form) return null;

  const update = (patch: Partial<AuditShippingCfg>) => setForm({ ...form, ...patch });

  return (
    <div className="panel" style={{ marginBottom: 14 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
        <h2 style={{ margin: 0 }}>External log shipping</h2>
        {form.enabled && form.last_ship_at && !form.last_ship_error && (
          <span className="tone-pill ok" title={`Last ship: ${new Date(form.last_ship_at).toLocaleString()} (${form.last_ship_count ?? 0} events)`}>healthy</span>
        )}
        {form.enabled && form.last_ship_error && (
          <span className="tone-pill danger" title={form.last_ship_error}>failing</span>
        )}
        {!form.enabled && <span className="tone-pill muted">disabled</span>}
      </div>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
        Stream every audit event to an external SIEM / log aggregator (syslog over UDP/TCP,
        or HTTP POST as JSON). Events batch up to the configured size or flush interval,
        whichever comes first. A stuck aggregator never blocks the audit-write path.
      </p>

      <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
        <input type="checkbox" checked={!!form.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
        <span style={{ fontSize: 12 }}>Enable shipping</span>
      </label>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 10 }}>
        <FormField label="Transport">
          <select value={form.type ?? ''} onChange={(e) => update({ type: e.target.value as 'syslog' | 'http' })} style={input} disabled={!form.enabled}>
            <option value="">—</option>
            <option value="http">HTTP POST (JSON)</option>
            <option value="syslog">Syslog (RFC 5424)</option>
          </select>
        </FormField>
        <FormField label="Endpoint">
          <input type="text" value={form.endpoint ?? ''} onChange={(e) => update({ endpoint: e.target.value })} style={input} disabled={!form.enabled}
            placeholder={form.type === 'syslog' ? 'udp://log.example.com:514' : 'https://logs.example.com/ingest'} />
        </FormField>
        {form.type === 'http' && (
          <>
            <FormField label="Bearer token (optional)">
              <input type="text" value={form.bearer_token ?? ''} onChange={(e) => update({ bearer_token: e.target.value })} style={input} disabled={!form.enabled}
                placeholder="(leave blank to keep existing)" />
            </FormField>
            <FormField label="Basic auth (user:pass, optional)">
              <input type="text" value={form.basic_auth ?? ''} onChange={(e) => update({ basic_auth: e.target.value })} style={input} disabled={!form.enabled}
                placeholder="(leave blank to keep existing)" />
            </FormField>
          </>
        )}
        {form.type === 'syslog' && (
          <>
            <FormField label="Syslog facility (0-23)">
              <input type="number" min={0} max={23} value={form.syslog_facility ?? 1} onChange={(e) => update({ syslog_facility: parseInt(e.target.value, 10) || 1 })} style={input} disabled={!form.enabled} />
            </FormField>
            <FormField label="Syslog app name">
              <input type="text" value={form.syslog_app_name ?? 'kvm-center'} onChange={(e) => update({ syslog_app_name: e.target.value })} style={input} disabled={!form.enabled} />
            </FormField>
          </>
        )}
        <FormField label="Batch size (events)">
          <input type="number" min={1} max={1000} value={form.batch_size ?? 50} onChange={(e) => update({ batch_size: parseInt(e.target.value, 10) || 50 })} style={input} disabled={!form.enabled} />
        </FormField>
        <FormField label="Flush interval (seconds)">
          <input type="number" min={1} max={3600} value={form.flush_seconds ?? 10} onChange={(e) => update({ flush_seconds: parseInt(e.target.value, 10) || 10 })} style={input} disabled={!form.enabled} />
        </FormField>
      </div>

      <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
        <button type="button" className="btn-primary-sm" disabled={!canManage || save.isPending} onClick={() => save.mutate(form)} title={!canManage ? 'Requires user.manage' : ''}>
          {save.isPending ? 'Saving…' : 'Save configuration'}
        </button>
        <button type="button" className="btn-secondary-sm" disabled={!canManage || !form.enabled || test.isPending} onClick={() => test.mutate()}>
          {test.isPending ? 'Testing…' : 'Send test event'}
        </button>
        {form.last_ship_at && (
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text-faded)' }}>
            Last flush: {new Date(form.last_ship_at).toLocaleString()} · {form.last_ship_count ?? 0} events
          </span>
        )}
      </div>
      {form.last_ship_error && (
        <div className="tone-panel danger" style={{ marginTop: 10, padding: '8px 12px', fontSize: 12 }}>
          Last error: {form.last_ship_error}
        </div>
      )}
    </div>
  );
}

function FormField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{ fontSize: 11, color: 'var(--text-faded)', textTransform: 'uppercase', letterSpacing: '0.08em' }}>{label}</span>
      {children}
    </label>
  );
}

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
      <ShippingPanel />
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
            {total === 0
              ? '—'
              : total <= limit
                ? `${total} matching event${total === 1 ? '' : 's'} (fits in one page of ${limit})`
                : `${offset + 1}–${Math.min(offset + limit, total)} of ${total} matching events`}
          </span>
          <button type="button" className="btn-secondary-sm" onClick={() => setOffset(Math.max(0, offset - limit))} disabled={offset === 0 || isFetching}>Newer</button>
          <button type="button" className="btn-secondary-sm" onClick={() => setOffset(offset + limit)} disabled={offset + limit >= total || isFetching}>Older</button>
        </div>
      </div>

      {/* Result table */}
      {isLoading && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading audit log…</div>}
      {error && <div className="tone-panel danger" style={{ padding: 12, fontSize: 12 }}>Failed to load: {(error as Error).message}</div>}
      {!isLoading && !error && (
        <div className="panel" style={{ padding: 0, overflow: 'auto', maxHeight: 'calc(100vh - 320px)' }}>
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
                  <th>Via</th>
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
                    <td>
                      <ViaPill detail={ev.detail} actorID={ev.actor_id} />
                    </td>
                    <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 12, color: 'var(--text-strong)' }}>{ev.action}</td>
                    <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 11, color: 'var(--text-muted)' }}>{ev.resource || '—'}</td>
                    <td style={{ fontSize: 11, color: 'var(--text-muted)', maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={detailTitle(ev.detail)}>
                      {formatDetail(stripVia(ev.detail))}
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

function ViaPill({ detail, actorID }: { detail?: Record<string, string>; actorID?: string }) {
  const via = detail?.via;
  if (via && via.startsWith('token:')) {
    return (
      <span className="tone-pill accent" style={{ fontFamily: "'SF Mono', monospace", fontSize: 10 }} title={via}>
        CLI · {via.slice('token:'.length)}
      </span>
    );
  }
  if (actorID === 'system') {
    return <span className="tone-pill warn" style={{ fontSize: 10 }}>System</span>;
  }
  return <span className="tone-pill" style={{ fontSize: 10 }}>WebUI</span>;
}

function stripVia(d?: Record<string, string>): Record<string, string> | undefined {
  if (!d) return d;
  const { via: _via, ...rest } = d;
  return rest;
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
