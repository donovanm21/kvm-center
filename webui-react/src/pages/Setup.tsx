import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';

/**
 * First-boot wizard (v5.20). Every step has a safe default so the
 * operator can click "Skip" at any point and still end up with a
 * working stack — Settings → General (et al) can tweak everything
 * the wizard touches after the fact.
 */

type StepKey = 'welcome' | 'hypervisor' | 'notifications' | 'metrics' | 'done';
const STEPS: { key: StepKey; label: string }[] = [
  { key: 'welcome',       label: 'Welcome' },
  { key: 'hypervisor',    label: 'First hypervisor' },
  { key: 'notifications', label: 'Notifications' },
  { key: 'metrics',       label: 'Metrics' },
  { key: 'done',          label: 'Done' },
];

export function Setup() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const { user } = useAuth();
  const [step, setStep] = useState<StepKey>('welcome');

  const state = useQuery({
    queryKey: ['setup-state'],
    queryFn: () => apiGet<{ complete: boolean; has_any_host: boolean }>('/api/setup/state'),
  });

  // Already finished (operator typed /setup manually after going live)?
  // Send them home so they don't accidentally mark themselves complete
  // again — the only side-effect would be a fresh audit entry.
  useEffect(() => {
    if (state.data?.complete) nav('/', { replace: true });
  }, [state.data?.complete, nav]);

  const complete = useMutation({
    mutationFn: () => apiPost<{ complete: boolean }>('/api/setup/complete', {}),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['setup-state'] });
      nav('/', { replace: true });
    },
  });

  const currentIdx = STEPS.findIndex((s) => s.key === step);
  const next = () => setStep(STEPS[Math.min(currentIdx + 1, STEPS.length - 1)].key);
  const skipAll = () => complete.mutate();

  return (
    <div style={shellStyle}>
      <div style={cardStyle}>
        <div style={headerStyle}>
          <div style={{ fontSize: 20, fontWeight: 700, color: 'var(--text-strong)' }}>
            Welcome, {user?.username ?? 'operator'}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>
            Set up kvm-center — every step is optional.
          </div>
        </div>

        <Steps current={step} onPick={setStep} />

        <div style={bodyStyle}>
          {step === 'welcome'       && <WelcomeStep onNext={next} />}
          {step === 'hypervisor'    && <HypervisorStep onNext={next} />}
          {step === 'notifications' && <NotificationsStep onNext={next} />}
          {step === 'metrics'       && <MetricsStep onNext={next} />}
          {step === 'done'          && <DoneStep onFinish={skipAll} pending={complete.isPending} />}
        </div>

        {step !== 'done' && (
          <div style={footerStyle}>
            <button type="button" className="btn-secondary-sm" onClick={skipAll} disabled={complete.isPending}>
              Skip the rest — take me to the dashboard
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function Steps({ current, onPick }: { current: StepKey; onPick: (k: StepKey) => void }) {
  return (
    <div style={stepsStyle}>
      {STEPS.map((s, i) => {
        const active = s.key === current;
        const done = STEPS.findIndex((x) => x.key === current) > i;
        return (
          <button
            key={s.key}
            type="button"
            onClick={() => onPick(s.key)}
            style={{
              ...stepChip,
              color: active ? 'var(--text-strong)' : done ? 'var(--text-body)' : 'var(--text-faded)',
              borderBottomColor: active ? 'var(--accent)' : 'transparent',
            }}
          >
            {i + 1}. {s.label}
          </button>
        );
      })}
    </div>
  );
}

/* ── Step 1 ─────────────────────────────────────────────── */
function WelcomeStep({ onNext }: { onNext: () => void }) {
  return (
    <>
      <h3 style={h3}>Your stack is running.</h3>
      <p style={p}>
        MongoDB, the server, and the download portal are all up. The next few steps
        help you connect a hypervisor and (optionally) turn on notifications + metrics.
      </p>
      <p style={p}>
        Nothing here is mandatory — <code style={kbd}>Settings</code> has every knob
        once you're done.
      </p>
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button type="button" className="btn-primary-sm" onClick={onNext}>Next →</button>
      </div>
    </>
  );
}

/* ── Step 2 ─────────────────────────────────────────────── */
function HypervisorStep({ onNext }: { onNext: () => void }) {
  const [hostID, setHostID] = useState('');
  const [token, setToken] = useState<string | null>(null);
  const [serverURL, setServerURL] = useState(() => `${location.protocol}//${location.hostname}:8090`);

  const state = useQuery({
    queryKey: ['setup-state'],
    queryFn: () => apiGet<{ has_any_host: boolean }>('/api/setup/state'),
    refetchInterval: 5_000,
  });

  const mint = useMutation({
    mutationFn: () => apiPost<{ enroll_token: string }>('/api/ca/enroll', { host_id: hostID.trim() }),
    onSuccess: (r) => setToken(r.enroll_token),
  });

  return (
    <>
      <h3 style={h3}>Enrol your first hypervisor</h3>
      <p style={p}>
        Run this on the hypervisor once you've minted a token below. The agent
        binary is served from this server's <code style={kbd}>/downloads/</code>;
        the install script opens the firewall, enrols the mTLS cert, and brings
        the node online within 30 seconds.
      </p>

      <label style={fieldLabel}>Hypervisor host ID (FQDN)</label>
      <input
        type="text"
        value={hostID}
        onChange={(e) => setHostID(e.target.value)}
        placeholder="e.g. hv01.cluster.local"
        style={input}
      />
      <div style={{ display: 'flex', gap: 8, marginTop: 10, flexWrap: 'wrap' }}>
        <button
          type="button"
          className="btn-primary-sm"
          disabled={!hostID.trim() || mint.isPending}
          onClick={() => mint.mutate()}
        >
          {mint.isPending ? 'Minting…' : 'Mint enrollment token'}
        </button>
        {state.data?.has_any_host && (
          <span className="tone-pill ok" style={{ alignSelf: 'center' }}>at least one host online</span>
        )}
      </div>

      {token && (
        <div style={{ marginTop: 14 }}>
          <div style={fieldLabel}>Run on the hypervisor</div>
          <pre style={pre}>{`curl -fsSL ${serverURL}/downloads/install-agent.sh | bash -s -- \\
  --server ${serverURL} \\
  --host-id ${hostID.trim()} \\
  --enroll-token ${token}`}</pre>
          <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 6 }}>
            Token is one-time, 10-minute TTL. Regenerate if it expires.
          </div>
        </div>
      )}

      {mint.error && (
        <div className="tone-panel danger" style={{ padding: 10, marginTop: 10, fontSize: 12 }}>
          {(mint.error as Error).message}
        </div>
      )}

      <div style={nextRow}>
        <label style={{ fontSize: 11, color: 'var(--text-faded)' }}>
          Server URL (for the one-liner)
          <input
            type="text"
            value={serverURL}
            onChange={(e) => setServerURL(e.target.value)}
            style={{ ...input, marginTop: 4, width: 260 }}
          />
        </label>
        <button type="button" className="btn-primary-sm" onClick={onNext}>
          {state.data?.has_any_host ? 'Next →' : 'Skip for now →'}
        </button>
      </div>
    </>
  );
}

/* ── Step 3 ─────────────────────────────────────────────── */
function NotificationsStep({ onNext }: { onNext: () => void }) {
  const qc = useQueryClient();
  const [kind, setKind] = useState<'discord' | 'slack' | 'webhook' | 'none'>('none');
  const [webhookURL, setWebhookURL] = useState('');
  const save = useMutation({
    mutationFn: (body: unknown) => apiPost('/api/settings/notifications', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  });
  const submit = () => {
    if (kind === 'none') return onNext();
    save.mutate({ enabled: true, channels: [{ kind, webhook_url: webhookURL.trim() }] });
    onNext();
  };
  return (
    <>
      <h3 style={h3}>Notifications <span style={opt}>· optional</span></h3>
      <p style={p}>
        Forward host + VM state changes to Discord, Slack, or a generic webhook.
        Skip now if you're not sure — the full channel editor is in{' '}
        <Link to="/settings/notifications" style={linkStyle}>Settings → Notifications</Link>.
      </p>
      <label style={fieldLabel}>Channel</label>
      <select value={kind} onChange={(e) => setKind(e.target.value as typeof kind)} style={input}>
        <option value="none">None (skip)</option>
        <option value="discord">Discord webhook</option>
        <option value="slack">Slack webhook</option>
        <option value="webhook">Generic JSON webhook</option>
      </select>
      {kind !== 'none' && (
        <>
          <label style={{ ...fieldLabel, marginTop: 10 }}>Webhook URL</label>
          <input type="text" value={webhookURL} onChange={(e) => setWebhookURL(e.target.value)} placeholder="https://…" style={input} />
        </>
      )}
      <div style={nextRow}>
        <span />
        <button type="button" className="btn-primary-sm" onClick={submit}>
          {kind === 'none' ? 'Skip →' : 'Save & next →'}
        </button>
      </div>
    </>
  );
}

/* ── Step 4 ─────────────────────────────────────────────── */
function MetricsStep({ onNext }: { onNext: () => void }) {
  const qc = useQueryClient();
  const [url, setURL] = useState('');
  const [org, setOrg] = useState('');
  const [bucket, setBucket] = useState('kvm-center');
  const [token, setToken] = useState('');
  const save = useMutation({
    mutationFn: (body: unknown) => apiPost('/api/settings/metrics', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['metrics'] }),
  });
  const hasCreds = url.trim() !== '' && org.trim() !== '' && token.trim() !== '';
  const submit = () => {
    if (!hasCreds) return onNext();
    save.mutate({ enabled: true, url: url.trim(), org: org.trim(), bucket: bucket.trim(), token: token.trim() });
    onNext();
  };
  return (
    <>
      <h3 style={h3}>Metrics <span style={opt}>· optional</span></h3>
      <p style={p}>
        Ship host / VM telemetry to an InfluxDB v2 bucket. Leave every field blank
        to skip — the in-UI sparklines still work without this. A ready-to-import
        Grafana dashboard lives in{' '}
        <Link to="/settings/metrics" style={linkStyle}>Settings → Metrics</Link>.
      </p>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <label style={fieldLabel}>InfluxDB URL
          <input type="text" value={url} onChange={(e) => setURL(e.target.value)} placeholder="http://influxdb:8086" style={{ ...input, marginTop: 4 }} />
        </label>
        <label style={fieldLabel}>Org
          <input type="text" value={org} onChange={(e) => setOrg(e.target.value)} style={{ ...input, marginTop: 4 }} />
        </label>
        <label style={fieldLabel}>Bucket
          <input type="text" value={bucket} onChange={(e) => setBucket(e.target.value)} style={{ ...input, marginTop: 4 }} />
        </label>
        <label style={fieldLabel}>Token
          <input type="password" value={token} onChange={(e) => setToken(e.target.value)} style={{ ...input, marginTop: 4 }} />
        </label>
      </div>
      <div style={nextRow}>
        <span />
        <button type="button" className="btn-primary-sm" onClick={submit}>
          {hasCreds ? 'Save & next →' : 'Skip →'}
        </button>
      </div>
    </>
  );
}

/* ── Step 5 ─────────────────────────────────────────────── */
function DoneStep({ onFinish, pending }: { onFinish: () => void; pending: boolean }) {
  return (
    <>
      <h3 style={h3}>All set.</h3>
      <p style={p}>
        Click below to finish the wizard and open your dashboard. You can revisit
        every step at any time through <code style={kbd}>Settings</code>, including{' '}
        <Link to="/settings/tls" style={linkStyle}>TLS</Link>,{' '}
        <Link to="/settings/tokens" style={linkStyle}>API tokens</Link>, and{' '}
        <Link to="/settings/providers" style={linkStyle}>identity providers</Link>.
      </p>
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button type="button" className="btn-primary-sm" onClick={onFinish} disabled={pending}>
          {pending ? 'Finishing…' : 'Open the dashboard →'}
        </button>
      </div>
    </>
  );
}

/* ── Styles ─────────────────────────────────────────────── */
const shellStyle: React.CSSProperties = {
  minHeight: '100vh',
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'center',
  padding: '48px 16px',
  background: 'var(--bg)',
};
const cardStyle: React.CSSProperties = {
  width: '100%', maxWidth: 780,
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 8,
  boxShadow: '0 20px 60px rgba(0,0,0,0.35)',
};
const headerStyle: React.CSSProperties = {
  padding: '20px 24px 10px', borderBottom: '1px solid var(--border-subtle)',
};
const stepsStyle: React.CSSProperties = {
  display: 'flex', gap: 4, padding: '8px 16px 0', borderBottom: '1px solid var(--border-subtle)',
  flexWrap: 'wrap',
};
const stepChip: React.CSSProperties = {
  padding: '8px 12px', fontSize: 11, fontWeight: 600, letterSpacing: '0.04em',
  textTransform: 'uppercase',
  background: 'transparent', border: 'none', cursor: 'pointer',
  borderBottom: '2px solid transparent',
};
const bodyStyle: React.CSSProperties = { padding: '20px 24px' };
const footerStyle: React.CSSProperties = {
  padding: '12px 24px', borderTop: '1px solid var(--border-subtle)',
  display: 'flex', justifyContent: 'flex-end',
};
const h3: React.CSSProperties = { margin: '0 0 8px 0', fontSize: 16, fontWeight: 600, color: 'var(--text-strong)' };
const p: React.CSSProperties = { margin: '0 0 12px 0', fontSize: 13, lineHeight: 1.6 };
const fieldLabel: React.CSSProperties = { display: 'block', fontSize: 11, color: 'var(--text-faded)', textTransform: 'uppercase', letterSpacing: '0.08em', marginTop: 4 };
const input: React.CSSProperties = { height: 32, padding: '0 10px', background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text-body)', fontSize: 12, fontFamily: 'inherit', width: '100%' };
const pre: React.CSSProperties = { background: 'var(--bg-input)', border: '1px solid var(--border)', borderRadius: 4, padding: '12px 14px', fontSize: 12, fontFamily: "'SF Mono', monospace", whiteSpace: 'pre-wrap', color: 'var(--text-strong)', overflowX: 'auto' };
const kbd: React.CSSProperties = { background: 'var(--bg-input)', padding: '1px 6px', borderRadius: 3, color: 'var(--accent-hover)', fontFamily: "'SF Mono', monospace", fontSize: 11 };
const opt: React.CSSProperties = { fontSize: 11, color: 'var(--text-faded)', fontWeight: 500, marginLeft: 8 };
const linkStyle: React.CSSProperties = { color: 'var(--accent-hover)' };
const nextRow: React.CSSProperties = { display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginTop: 14, gap: 12 };
