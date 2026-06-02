import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost, apiPut } from '@/api/client';
import { useToast } from '@/hooks/useToast';
import { qk } from '@/queryKeys';

interface Channel {
  id: string;
  name: string;
  kind: 'webhook' | 'email' | 'discord-gateway';
  enabled: boolean;
  webhook_url?: string;
  webhook_secret?: string;
  webhook_format?: '' | 'discord' | 'slack' | 'generic';
  email_to?: string;
  email_from?: string;
  event_types?: Record<string, boolean>;
  last_send_at?: string;
  last_send_error?: string;
  last_send_count?: number;
}

interface NotifConfig {
  channels: Channel[];
  event_types: Record<string, boolean>;
  smtp_host?: string;
  smtp_port?: number;
  smtp_user?: string;
  smtp_password?: string;
  smtp_from?: string;
  smtp_starttls?: boolean;
}

interface Reply {
  config: NotifConfig;
  all_types: string[];
}

const EVENT_LABELS: Record<string, string> = {
  'vm.crashed':           'VM crashed',
  'vm.recovered':         'VM recovered',
  'host.offline':         'Host offline',
  'host.recovered':       'Host recovered',
  'migration.complete':   'Migration complete',
  'migration.failed':     'Migration failed',
  'tls.expiring':             'Server TLS cert expiring',
  'ca.expiring':              'Cluster CA expiring',
  'hypervisor.cert.expiring': 'Hypervisor cert expiring',
  'snapshot.policy.failed':   'Snapshot policy failed',
};

export function NotificationsTab() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const q = useQuery({
    queryKey: qk.notifications(),
    queryFn: () => apiGet<Reply>('/api/settings/notifications'),
  });
  // Read the gateway status here too so the "+ Add Discord Gateway
  // channel" button can disable itself when the pairing isn't usable
  // — react-query dedupes by queryKey, so the panel below shares
  // this fetch.
  const gw = useQuery({
    queryKey: qk.discordGateway(),
    queryFn: () => apiGet<DGStatus>('/api/settings/discord-gateway'),
    refetchInterval: 5_000,
  });
  const [form, setForm] = useState<NotifConfig | null>(null);

  useEffect(() => {
    if (q.data) {
      const cfg = { ...q.data.config };
      if (!cfg.event_types) cfg.event_types = {};
      if (!cfg.channels) cfg.channels = [];
      setForm(cfg);
    }
  }, [q.data]);

  const save = useMutation({
    mutationFn: (cfg: NotifConfig) => apiPut('/api/settings/notifications', cfg),
    onSuccess: () => {
      toast("success", 'Notification settings saved');
      qc.invalidateQueries({ queryKey: qk.notifications() });
    },
    onError: (e: Error) => toast("error", 'Save failed: ' + e.message),
  });

  const test = useMutation({
    mutationFn: (channelID: string) =>
      apiPost('/api/settings/notifications/test', { channel_id: channelID }),
    onSuccess: () => {
      toast("success", 'Test notification sent');
      qc.invalidateQueries({ queryKey: qk.notifications() });
    },
    onError: (e: Error) => toast("error", 'Test failed: ' + e.message),
  });

  if (q.isLoading || !form) return <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading…</div>;
  if (q.error) return <div className="tone-pill danger">Failed to load: {(q.error as Error).message}</div>;

  const update = (patch: Partial<NotifConfig>) => setForm({ ...form, ...patch });
  const updateChannel = (idx: number, patch: Partial<Channel>) => {
    const next = form.channels.slice();
    next[idx] = { ...next[idx], ...patch };
    update({ channels: next });
  };
  const addChannel = (kind: 'webhook' | 'email' | 'discord-gateway') => {
    let name = 'New webhook';
    if (kind === 'email') name = 'New email';
    else if (kind === 'discord-gateway') name = 'Discord Gateway';
    update({
      channels: [
        ...form.channels,
        { id: '', name, kind, enabled: true },
      ],
    });
  };
  const removeChannel = (idx: number) => {
    update({ channels: form.channels.filter((_, i) => i !== idx) });
  };
  const allTypes = q.data!.all_types;

  return (
    <div style={{ display: 'grid', gap: 16 }}>
      {/* v5.60.0 — Gateways hero. Operators get the strongest signal
          to opt into a gateway over wiring N individual webhooks,
          which is why it lives at the top of the page now. */}
      <div className="panel" style={{ borderLeft: '3px solid var(--accent)' }}>
        <div style={sectionHeader}>Gateways</div>
        <p style={{ margin: '0 0 10px 0', fontSize: 11, color: 'var(--text-muted)' }}>
          A gateway fans events to a single destination (currently Discord — Slack and Teams to follow). Recommended over wiring individual webhooks: one pair-and-forget setup, automatic retries, central rate-limit, and an operator-visible health badge. If you'd rather route events à la carte, the Channels section below still works.
        </p>
        <DiscordGatewayPanel />
      </div>

      <div className="panel">
        <div style={sectionHeader}>Default event subscriptions</div>
        <p style={{ margin: '0 0 10px 0', fontSize: 11, color: 'var(--text-muted)' }}>
          Channels without their own subscription use these defaults. Override per-channel below to route different events to different places.
        </p>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))', gap: 6 }}>
          {allTypes.map((t) => (
            <label key={t} style={checkboxRow}>
              <input
                type="checkbox"
                checked={!!form.event_types[t]}
                onChange={(e) => update({ event_types: { ...form.event_types, [t]: e.target.checked } })}
              />
              <span style={{ fontWeight: 500 }}>
                {EVENT_LABELS[t] || t} <span style={{ color: 'var(--text-faded)', fontWeight: 400, fontSize: 11 }}>({t})</span>
              </span>
            </label>
          ))}
        </div>
      </div>

      <div className="panel">
        <div style={sectionHeader}>Channels</div>
        {form.channels.length === 0 && (
          <div style={{ fontSize: 12, color: 'var(--text-muted)', marginBottom: 8 }}>
            No channels configured. Add a webhook or email destination below.
          </div>
        )}
        <div style={{ display: 'grid', gap: 12 }}>
          {form.channels.map((ch, idx) => (
            <div key={idx} style={channelCardStyle}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
                <input
                  type="checkbox"
                  checked={ch.enabled}
                  onChange={(e) => updateChannel(idx, { enabled: e.target.checked })}
                  title="Enable / disable"
                />
                <input
                  type="text"
                  value={ch.name}
                  onChange={(e) => updateChannel(idx, { name: e.target.value })}
                  placeholder="Channel name"
                  style={{ ...inputStyle, flex: 1, fontWeight: 500 }}
                />
                <span className="tone-pill" style={{ fontSize: 10 }}>{ch.kind}</span>
                {ch.id && (
                  <button
                    type="button"
                    className="btn-secondary-sm"
                    disabled={test.isPending}
                    onClick={() => test.mutate(ch.id)}
                  >
                    Send test
                  </button>
                )}
                <button type="button" className="btn-danger-sm" onClick={() => removeChannel(idx)}>
                  Remove
                </button>
              </div>
              {ch.kind === 'webhook' && (
                <div style={{ display: 'grid', gap: 6 }}>
                  <Field label="Webhook URL">
                    <input
                      type="url"
                      value={ch.webhook_url || ''}
                      onChange={(e) => updateChannel(idx, { webhook_url: e.target.value })}
                      placeholder="https://hooks.example.com/…"
                      style={inputStyle}
                    />
                  </Field>
                  <Field label="Payload format">
                    <select
                      value={ch.webhook_format || ''}
                      onChange={(e) => updateChannel(idx, { webhook_format: e.target.value as Channel['webhook_format'] })}
                      style={inputStyle}
                    >
                      <option value="">Auto-detect (by URL)</option>
                      <option value="discord">Discord (rich embed)</option>
                      <option value="slack">Slack (attachment)</option>
                      <option value="generic">Generic JSON (n8n / Zapier / custom)</option>
                    </select>
                  </Field>
                  <Field label="HMAC secret (optional)">
                    <input
                      type="password"
                      value={ch.webhook_secret || ''}
                      onChange={(e) => updateChannel(idx, { webhook_secret: e.target.value })}
                      placeholder="Secret used to sign X-KVMCenter-Signature"
                      style={inputStyle}
                    />
                  </Field>
                </div>
              )}
              {ch.kind === 'email' && (
                <div style={{ display: 'grid', gap: 6 }}>
                  <Field label="To (comma-separated)">
                    <input
                      type="text"
                      value={ch.email_to || ''}
                      onChange={(e) => updateChannel(idx, { email_to: e.target.value })}
                      placeholder="oncall@example.com, ops@example.com"
                      style={inputStyle}
                    />
                  </Field>
                  <Field label="From (override global)">
                    <input
                      type="text"
                      value={ch.email_from || ''}
                      onChange={(e) => updateChannel(idx, { email_from: e.target.value })}
                      placeholder="kvm-center@example.com"
                      style={inputStyle}
                    />
                  </Field>
                </div>
              )}
              {ch.kind === 'discord-gateway' && (
                <div style={{ fontSize: 11, color: 'var(--text-muted)', padding: '6px 0' }}>
                  Routes through the paired Discord Gateway below. Configure pairing
                  in the <strong>Discord Gateway</strong> panel — this channel just
                  toggles which event types fan into it.
                </div>
              )}
              <ChannelSubscription
                ch={ch}
                allTypes={allTypes}
                onToggle={(t, on) => {
                  const next = { ...(ch.event_types || {}) };
                  next[t] = on;
                  updateChannel(idx, { event_types: next });
                }}
                onResetToDefault={() => updateChannel(idx, { event_types: undefined })}
                onCopyFromDefault={() => updateChannel(idx, { event_types: { ...form.event_types } })}
              />
              {(ch.last_send_at || ch.last_send_error) && (
                <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text-faded)' }}>
                  Last send: {ch.last_send_at || '—'}
                  {ch.last_send_count ? ` · ${ch.last_send_count} successes` : ''}
                  {ch.last_send_error && (
                    <div className="tone-pill danger" style={{ marginTop: 4, display: 'inline-block' }}>
                      {ch.last_send_error}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
        <div style={{ display: 'flex', gap: 6, marginTop: 12 }}>
          <button type="button" className="btn-secondary-sm" onClick={() => addChannel('webhook')}>
            + Add webhook
          </button>
          <button type="button" className="btn-secondary-sm" onClick={() => addChannel('email')}>
            + Add email
          </button>
          <button
            type="button"
            className="btn-secondary-sm"
            disabled={!gw.data || !gw.data.enabled}
            title={
              !gw.data || !gw.data.enabled
                ? 'Configure + enable the Discord Gateway panel below first.'
                : gw.data.status !== 'healthy'
                  ? 'Channel will queue events until the gateway is healthy. Status: ' + (gw.data.status || 'unknown')
                  : ''
            }
            onClick={() => addChannel('discord-gateway')}
          >
            + Add Discord Gateway channel
          </button>
        </div>
      </div>

      <div className="panel">
        <div style={sectionHeader}>SMTP (for email channels)</div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          <Field label="SMTP host">
            <input type="text" value={form.smtp_host || ''} onChange={(e) => update({ smtp_host: e.target.value })} placeholder="smtp.example.com" style={inputStyle} />
          </Field>
          <Field label="SMTP port">
            <input type="number" value={form.smtp_port || 587} onChange={(e) => update({ smtp_port: parseInt(e.target.value || '0', 10) })} style={inputStyle} />
          </Field>
          <Field label="SMTP user">
            <input type="text" value={form.smtp_user || ''} onChange={(e) => update({ smtp_user: e.target.value })} style={inputStyle} />
          </Field>
          <Field label="SMTP password">
            <input type="password" value={form.smtp_password || ''} onChange={(e) => update({ smtp_password: e.target.value })} style={inputStyle} />
          </Field>
          <Field label="From address">
            <input type="text" value={form.smtp_from || ''} onChange={(e) => update({ smtp_from: e.target.value })} placeholder="kvm-center@example.com" style={inputStyle} />
          </Field>
          <label style={{ ...checkboxRow, alignSelf: 'end' }}>
            <input type="checkbox" checked={!!form.smtp_starttls} onChange={(e) => update({ smtp_starttls: e.target.checked })} />
            <span>Use STARTTLS</span>
          </label>
        </div>
      </div>

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button
          type="button"
          className="btn-primary-sm"
          onClick={() => save.mutate(form)}
          disabled={save.isPending}
        >
          {save.isPending ? 'Saving…' : 'Save settings'}
        </button>
      </div>
    </div>
  );
}

function ChannelSubscription({
  ch, allTypes, onToggle, onResetToDefault, onCopyFromDefault,
}: {
  ch: Channel;
  allTypes: string[];
  onToggle: (t: string, on: boolean) => void;
  onResetToDefault: () => void;
  onCopyFromDefault: () => void;
}) {
  const [open, setOpen] = useState(false);
  const hasOverride = !!ch.event_types && Object.keys(ch.event_types).length > 0;
  const enabledCount = hasOverride ? Object.values(ch.event_types!).filter(Boolean).length : null;
  return (
    <div style={{ marginTop: 10, paddingTop: 10, borderTop: '1px dashed var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11 }}>
        <button
          type="button"
          className="btn-secondary-sm"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? '▾' : '▸'} Event routing
        </button>
        {hasOverride
          ? <span className="tone-pill accent" style={{ fontSize: 10 }}>override: {enabledCount} events</span>
          : <span className="tone-pill" style={{ fontSize: 10 }}>using defaults</span>}
        {hasOverride && (
          <button type="button" className="btn-secondary-sm" onClick={onResetToDefault}>
            Clear override
          </button>
        )}
        {!hasOverride && (
          <button type="button" className="btn-secondary-sm" onClick={onCopyFromDefault}>
            Start from defaults
          </button>
        )}
      </div>
      {open && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 4, marginTop: 8 }}>
          {allTypes.map((t) => (
            <label key={t} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--text-body)' }}>
              <input
                type="checkbox"
                checked={!!ch.event_types?.[t]}
                onChange={(e) => onToggle(t, e.target.checked)}
              />
              <span>
                {EVENT_LABELS[t] || t}{' '}
                <span style={{ color: 'var(--text-faded)', fontSize: 10 }}>({t})</span>
              </span>
            </label>
          ))}
        </div>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text-muted)' }}>
      {label}
      {children}
    </label>
  );
}

const sectionHeader: React.CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.06em',
  color: 'var(--text-faded)',
  marginBottom: 12,
};
const checkboxRow: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 8,
  fontSize: 12,
  color: 'var(--text-body)',
  padding: '4px 0',
};
const channelCardStyle: React.CSSProperties = {
  border: '1px solid var(--border)',
  borderRadius: 6,
  padding: 12,
  background: 'var(--surface-2)',
};
const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
};

// ── Discord Gateway panel (v5.52.0) ─────────────────────────────
// Surface the paired-gateway integration as its own panel under
// the channels list. Operator workflow:
//   1. toggle on, type host + port + verify_tls
//   2. save → status flips Saving → Pending while gateway-side approval is awaited
//   3. open gateway webui, approve → status flips to Healthy within ~30 s
//   4. add a "discord-gateway" channel above and tick the events to fan in

interface DGStatus {
  enabled: boolean;
  host?: string;
  port?: number;
  tls?: boolean;
  verify_tls?: boolean;
  ship_audit_events?: boolean;
  ship_audit_failed_only?: boolean;
  registration_id?: string;
  webhook_id?: string;
  status?: string;
  last_health_at?: string;
  last_health_error?: string;
  last_send_at?: string;
  last_send_error?: string;
  last_send_count?: number;
  dropped_events?: number;
  pending_since?: string;
}

// Recognise TLS / x509 verification errors from the gateway's last
// healthz / pair-request attempt. Used to surface a one-click
// "skip + re-pair" helper without the operator having to deduce
// from the raw error text that the fix is to flip the checkbox.
function tlsErrInError(err: string | undefined | null): boolean {
  if (!err) return false;
  const s = err.toLowerCase();
  return s.includes('x509') ||
    s.includes('tls: failed to verify') ||
    s.includes('certificate signed by unknown authority') ||
    s.includes('certificate is not trusted');
}

const DG_STATUS_TONE: Record<string, string> = {
  disabled:    'tone-pill',
  saving:      'tone-pill warn',
  pending:     'tone-pill warn',
  approved:    'tone-pill warn',
  connecting:  'tone-pill warn',
  healthy:     'tone-pill ok',
  unreachable: 'tone-pill danger',
  revoked:     'tone-pill danger',
  timeout:     'tone-pill danger',
};

function DiscordGatewayPanel() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const q = useQuery({
    queryKey: qk.discordGateway(),
    queryFn: () => apiGet<DGStatus>('/api/settings/discord-gateway'),
    refetchInterval: 5_000, // status moves on its own — keep the badge live
  });
  const [form, setForm] = useState<DGStatus | null>(null);

  useEffect(() => {
    if (q.data && form === null) {
      setForm({
        enabled: q.data.enabled,
        host: q.data.host || '',
        port: q.data.port || 8090,
        tls: q.data.tls ?? true,
        verify_tls: q.data.verify_tls ?? false,
        ship_audit_events: q.data.ship_audit_events ?? false,
        // Default failed-only ON when the firehose is first flipped on,
        // so a busy fleet's successful-action stream doesn't drown
        // the channel.
        ship_audit_failed_only: q.data.ship_audit_failed_only ?? true,
      });
    }
  }, [q.data, form]);

  const save = useMutation({
    mutationFn: (cfg: DGStatus) => apiPut<DGStatus>('/api/settings/discord-gateway', cfg),
    onSuccess: () => {
      toast('success', 'Discord Gateway settings saved');
      qc.invalidateQueries({ queryKey: qk.discordGateway() });
    },
    onError: (e: Error) => toast('error', 'Save failed: ' + e.message),
  });
  // Re-pair always ships the current form so the user's TLS posture
  // toggle takes effect even if they didn't click Save first
  // (otherwise saved state wins and the operator-friendly "uncheck +
  // re-pair" workflow leaks the previous verify_tls value).
  const repair = useMutation({
    mutationFn: (cfg: DGStatus) => apiPost<DGStatus>('/api/settings/discord-gateway/pair', cfg),
    onSuccess: () => {
      toast('info', 'Re-pair started — approve in the gateway UI');
      qc.invalidateQueries({ queryKey: qk.discordGateway() });
    },
    onError: (e: Error) => toast('error', 'Pair failed: ' + e.message),
  });
  const revoke = useMutation({
    mutationFn: () => apiPost<{ status: string }>('/api/settings/discord-gateway/revoke', {}),
    onSuccess: () => {
      toast('success', 'Pairing revoked');
      qc.invalidateQueries({ queryKey: qk.discordGateway() });
    },
    onError: (e: Error) => toast('error', 'Revoke failed: ' + e.message),
  });
  const test = useMutation({
    mutationFn: () => apiPost<{ status: string }>('/api/settings/discord-gateway/test', {}),
    onSuccess: () => toast('success', 'Test card shipped — check your Discord audit channel'),
    onError: (e: Error) => toast('error', 'Test failed: ' + e.message),
  });

  if (!form) return null;

  const status = q.data?.status || 'disabled';
  const toneCls = DG_STATUS_TONE[status] || 'tone-pill';

  return (
    // v5.60.0 — panel wrapper moved to the parent (Gateways hero in
    // NotificationsTab) so the DG panel doesn't double-card when
    // rendered inside the new hero.
    <div>
      <div style={{ ...sectionHeader, display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 12 }}>
        <span>Discord</span>
        <span className={toneCls} style={{ fontSize: 10 }}>{status}</span>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
        <Field label="Enabled">
          <label style={checkboxRow}>
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
            />
            <span style={{ fontSize: 12 }}>route notifications through the gateway</span>
          </label>
        </Field>
        <Field label="TLS">
          <label style={checkboxRow}>
            <input
              type="checkbox"
              checked={form.tls ?? true}
              onChange={(e) => setForm({ ...form, tls: e.target.checked })}
            />
            <span style={{ fontSize: 12 }}>HTTPS (gateway default)</span>
          </label>
        </Field>
        <Field label="Host">
          <input
            type="text"
            value={form.host || ''}
            onChange={(e) => setForm({ ...form, host: e.target.value })}
            placeholder="discord-gw.lan"
            style={inputStyle}
          />
        </Field>
        <Field label="Port">
          <input
            type="number"
            value={form.port || 0}
            onChange={(e) => setForm({ ...form, port: parseInt(e.target.value || '0', 10) })}
            placeholder="8090"
            style={inputStyle}
          />
        </Field>
        <Field label="TLS verification">
          <label style={checkboxRow}>
            {/* Inverted semantic: the checkbox value is "skip", which
                is what operators with self-signed gateways are reaching
                for. Persisted as verify_tls (positive) — flip on bind. */}
            <input
              type="checkbox"
              checked={!(form.verify_tls ?? false)}
              onChange={(e) => setForm({ ...form, verify_tls: !e.target.checked })}
            />
            <span style={{ fontSize: 12 }}>
              {form.verify_tls
                ? 'verifying gateway cert against trust store (uncheck to skip — for self-signed gateways)'
                : 'skip TLS verification — required when the gateway uses a self-signed cert'}
            </span>
          </label>
        </Field>
      </div>
      {/* Audit firehose toggles (v5.53.0). Off by default — fleets with
          chatty audit logs would otherwise produce thousands of cards
          a day. Failed-only sub-toggle keeps the default useful: only
          incident signal lands in Discord. */}
      <div style={{ marginTop: 10, padding: '10px 12px', background: 'var(--surface-2)', border: '1px solid var(--border)', borderRadius: 6 }}>
        <div style={{ fontSize: 11, color: 'var(--text-muted)', marginBottom: 6 }}>
          <strong>Audit log firehose</strong> — independent of the channels above.
          When on, every audit log entry ships to the gateway as a card.
        </div>
        <label style={checkboxRow}>
          <input
            type="checkbox"
            checked={form.ship_audit_events ?? false}
            onChange={(e) => setForm({ ...form, ship_audit_events: e.target.checked })}
          />
          <span style={{ fontSize: 12 }}>Ship audit log entries to Discord</span>
        </label>
        {form.ship_audit_events && (
          <label style={{ ...checkboxRow, marginLeft: 24 }}>
            <input
              type="checkbox"
              checked={form.ship_audit_failed_only ?? true}
              onChange={(e) => setForm({ ...form, ship_audit_failed_only: e.target.checked })}
            />
            <span style={{ fontSize: 12 }}>Failed events only (recommended — quieter)</span>
          </label>
        )}
      </div>
      <div style={{ marginTop: 10, display: 'flex', gap: 6 }}>
        <button
          type="button"
          className="btn-primary-sm"
          disabled={save.isPending}
          onClick={() => save.mutate(form)}
        >
          {save.isPending ? 'Saving…' : 'Save'}
        </button>
        <button
          type="button"
          className="btn-secondary-sm"
          disabled={!q.data?.enabled || repair.isPending}
          onClick={() => repair.mutate(form)}
        >
          Re-pair
        </button>
        <button
          type="button"
          className="btn-secondary-sm"
          disabled={!q.data?.enabled || test.isPending || status !== 'healthy'}
          onClick={() => test.mutate()}
        >
          Send test
        </button>
        <button
          type="button"
          className="btn-danger-sm"
          disabled={!q.data?.enabled || revoke.isPending}
          onClick={() => revoke.mutate()}
        >
          Revoke
        </button>
      </div>
      {(q.data?.last_health_error || q.data?.last_send_error) && (
        <div style={{ marginTop: 10, fontSize: 11 }}>
          {q.data?.last_health_error && (
            <div className="tone-pill danger" style={{ display: 'inline-block', marginRight: 6 }}>
              healthz: {q.data.last_health_error}
            </div>
          )}
          {q.data?.last_send_error && (
            <div className="tone-pill danger" style={{ display: 'inline-block' }}>
              send: {q.data.last_send_error}
            </div>
          )}
        </div>
      )}
      {/* TLS-error auto-fix banner — most common operator stumble. */}
      {form.verify_tls && tlsErrInError(q.data?.last_health_error) && (
        <div
          className="tone-panel warn"
          style={{ marginTop: 10, padding: '10px 12px', borderRadius: 6, fontSize: 12 }}
        >
          The gateway's TLS certificate isn't trusted by this server.
          Most operators run the gateway with its self-signed cert —
          tick <strong>Skip TLS verification</strong> above and re-pair.
          <div style={{ marginTop: 8 }}>
            <button
              type="button"
              className="btn-primary-sm"
              disabled={repair.isPending}
              onClick={() => {
                const next = { ...form, verify_tls: false };
                setForm(next);
                repair.mutate(next);
              }}
            >
              Disable TLS verification + re-pair
            </button>
          </div>
        </div>
      )}
      {q.data?.last_send_at && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text-faded)' }}>
          Last successful send: {q.data.last_send_at}
          {q.data.last_send_count ? ` · ${q.data.last_send_count} cards` : ''}
          {q.data.dropped_events ? ` · ${q.data.dropped_events} dropped (queue full or unhealthy)` : ''}
        </div>
      )}
      {status === 'pending' && (
        <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text-muted)' }}>
          Open the gateway's web UI → <strong>Pairings</strong>, click <strong>Approve</strong>.
          Status flips to <strong>healthy</strong> within 30 s.
        </div>
      )}
    </div>
  );
}
