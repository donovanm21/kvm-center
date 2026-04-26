import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';
import { qk } from '@/queryKeys';

interface BackendInfo {
  type: 'json' | 'mongo';
  endpoint?: string;
  size_bytes?: number;
  last_modified?: string;
  healthy: boolean;
  error?: string;
}

/**
 * Settings → Database (v5.1): view the currently-active persistence
 * backend, test a candidate MongoDB URL without committing, and
 * migrate data between backends. Actual backend switching is done by
 * restarting the server with a new -mongo-url (or KVM_CENTER_MONGO_URL
 * env) — the UI provides the data move and the reachability probe.
 */
export function DatabaseTab() {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const qc = useQueryClient();
  const canManage = hasPerm('user.manage');

  const status = useQuery({
    queryKey: qk.dbStatus(),
    queryFn: () => apiGet<BackendInfo>('/api/settings/database'),
    refetchInterval: 30_000,
  });

  // "Test connection" form state — lets the operator probe a
  // candidate URL before committing it to docker-compose / env.
  const [testURL, setTestURL] = useState('');
  const [testDB, setTestDB] = useState('kvm-center');
  const [testResult, setTestResult] = useState<BackendInfo | null>(null);

  const test = useMutation({
    mutationFn: () => apiPost<BackendInfo>('/api/settings/database/test', { url: testURL, db: testDB }),
    onSuccess: (info) => {
      setTestResult(info);
      if (info.healthy) toast('success', 'Connection succeeded');
      else toast('error', `Connection failed: ${info.error}`);
    },
    onError: (e) => toast('error', `Test failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  // Migration: copies the live backend's contents to a target. The
  // live backend keeps serving reads/writes; the target becomes the
  // "ready to cut over" copy. Actual cutover is a server restart.
  const [migrateTo, setMigrateTo] = useState<'json' | 'mongo'>('mongo');
  const [migrateURL, setMigrateURL] = useState('');
  const [migrateDB, setMigrateDB] = useState('kvm-center');
  const [migratePath, setMigratePath] = useState('/var/lib/kvm-center/data.json');

  const migrate = useMutation({
    mutationFn: () => apiPost<{ status: string; from: string; to: string }>(
      '/api/settings/database/migrate',
      migrateTo === 'mongo'
        ? { to: 'mongo', url: migrateURL, db: migrateDB }
        : { to: 'json', path: migratePath },
    ),
    onSuccess: (r) => {
      toast('success', `Migrated from ${r.from} → ${r.to}. Restart the server pointed at the new backend to cut over.`);
      qc.invalidateQueries({ queryKey: qk.dbStatus() });
    },
    onError: (e) => toast('error', `Migrate failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  if (status.isLoading) return <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading database info…</div>;
  if (status.error) return <div className="tone-panel danger" style={{ padding: 12, fontSize: 12 }}>Failed to load: {(status.error as Error).message}</div>;

  const info = status.data!;
  const fmtBytes = (b?: number) => {
    if (!b) return '—';
    if (b > 1_000_000_000) return `${(b / 1e9).toFixed(2)} GB`;
    if (b > 1_000_000) return `${(b / 1e6).toFixed(1)} MB`;
    if (b > 1000) return `${(b / 1e3).toFixed(1)} kB`;
    return `${b} B`;
  };

  return (
    <>
      {/* Current backend card */}
      <div className={`tone-panel ${info.healthy ? 'ok' : 'danger'}`} style={{ marginBottom: 16, padding: '12px 16px', display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.08em', opacity: 0.8 }}>Active backend</div>
        <div style={{ fontSize: 16, fontWeight: 600 }}>{info.type.toUpperCase()}</div>
        <div style={{ flex: 1 }} />
        <span className={`tone-pill ${info.healthy ? 'ok' : 'danger'}`}>{info.healthy ? 'healthy' : 'unreachable'}</span>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2 style={{ margin: 0, marginBottom: 10 }}>Current persistence</h2>
        <KV label="Type" value={info.type} />
        <KV label="Endpoint" value={info.endpoint} mono wrap />
        <KV label="Size" value={fmtBytes(info.size_bytes)} />
        <KV label="Last modified" value={info.last_modified ? new Date(info.last_modified).toLocaleString() : '—'} />
        {info.error && <KV label="Error" value={info.error} wrap />}
        <p style={{ fontSize: 11, color: 'var(--text-faded)', margin: '10px 0 0 0', lineHeight: 1.5 }}>
          To switch between JSON and MongoDB, restart the server with (or without) <code>-mongo-url</code>
          / <code>KVM_CENTER_MONGO_URL</code>. The docker-compose setup already sets this, so a
          vanilla redeploy via <code>bootstrap.sh --deploy</code> uses Mongo automatically once{' '}
          <code>.env</code> is populated.
        </p>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2 style={{ margin: 0, marginBottom: 4 }}>Test MongoDB connection</h2>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
          Probe a candidate URI without swapping the active backend. Useful before editing
          <code> KVM_CENTER_MONGO_URL</code> on the server host.
        </p>
        <Field label="Mongo URI">
          <input
            type="text"
            placeholder="mongodb://user:pass@host:27017/kvm-center?authSource=admin"
            value={testURL}
            onChange={(e) => setTestURL(e.target.value)}
            style={input}
          />
        </Field>
        <Field label="Database">
          <input type="text" value={testDB} onChange={(e) => setTestDB(e.target.value)} style={input} />
        </Field>
        <div style={{ marginTop: 10 }}>
          <button
            type="button"
            className="btn-secondary-sm"
            disabled={!canManage || !testURL || test.isPending}
            onClick={() => test.mutate()}
            title={!canManage ? 'Requires user.manage' : ''}
          >
            {test.isPending ? 'Testing…' : 'Test connection'}
          </button>
        </div>
        {testResult && (
          <div className={`tone-panel ${testResult.healthy ? 'ok' : 'danger'}`} style={{ marginTop: 12, padding: 10, fontSize: 12 }}>
            <strong>{testResult.healthy ? '✓ Reachable' : '✕ Unreachable'}</strong>
            {testResult.endpoint && <div style={{ marginTop: 4, fontFamily: "'SF Mono', monospace" }}>{testResult.endpoint}</div>}
            {testResult.error && <div style={{ marginTop: 4 }}>{testResult.error}</div>}
            {typeof testResult.size_bytes === 'number' && <div style={{ marginTop: 4 }}>Size: {fmtBytes(testResult.size_bytes)}</div>}
          </div>
        )}
      </div>

      <div className="panel">
        <h2 style={{ margin: 0, marginBottom: 4 }}>Migrate data</h2>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
          Copy everything from the active backend ({info.type}) to a target. This does NOT swap the
          active backend — after the copy succeeds, restart the server pointed at the target URL/path
          to cut over. The source backend is left untouched so rollback is a one-flag change.
        </p>
        <Field label="Target backend">
          <select value={migrateTo} onChange={(e) => setMigrateTo(e.target.value as 'json' | 'mongo')} style={input}>
            <option value="mongo">MongoDB</option>
            <option value="json">JSON file</option>
          </select>
        </Field>
        {migrateTo === 'mongo' ? (
          <>
            <Field label="Mongo URI">
              <input type="text" value={migrateURL} onChange={(e) => setMigrateURL(e.target.value)} placeholder="mongodb://user:pass@host:27017/kvm-center?authSource=admin" style={input} />
            </Field>
            <Field label="Database">
              <input type="text" value={migrateDB} onChange={(e) => setMigrateDB(e.target.value)} style={input} />
            </Field>
          </>
        ) : (
          <Field label="Path (on server host)">
            <input type="text" value={migratePath} onChange={(e) => setMigratePath(e.target.value)} style={input} />
          </Field>
        )}
        <div style={{ marginTop: 10 }}>
          <button
            type="button"
            className="btn-primary-sm"
            disabled={!canManage || migrate.isPending || (migrateTo === 'mongo' ? !migrateURL : !migratePath)}
            onClick={() => {
              if (confirm(`Copy all ${info.type} data to the target ${migrateTo} backend? The active backend stays unchanged; you'll need to restart the server to cut over.`)) {
                migrate.mutate();
              }
            }}
            title={!canManage ? 'Requires user.manage' : ''}
          >
            {migrate.isPending ? 'Migrating…' : `Migrate ${info.type} → ${migrateTo}`}
          </button>
        </div>
      </div>
    </>
  );
}

function KV({ label, value, mono, wrap }: { label: string; value?: string; mono?: boolean; wrap?: boolean }) {
  return (
    <div style={{ display: 'flex', padding: '6px 0', gap: 12 }}>
      <div style={{ width: 140, fontSize: 11, color: 'var(--text-muted)' }}>{label}</div>
      <div style={{ fontSize: 12, color: 'var(--text-body)', fontFamily: mono ? "'SF Mono', monospace" : 'inherit', wordBreak: wrap ? 'break-all' : 'normal', flex: 1 }}>
        {value || '—'}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 4 }}>{label}</div>
      {children}
    </div>
  );
}

const input: React.CSSProperties = {
  width: '100%', height: 32, padding: '0 10px',
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontSize: 12, fontFamily: 'inherit',
};
