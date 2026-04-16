import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost, apiPut } from '@/api/client';
import { useToast } from '@/hooks/useToast';

interface Channel {
  id: string;
  name: string;
  kind: 'webhook' | 'email';
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
  'tls.expiring':         'TLS cert expiring (<30 days)',
  'snapshot.policy.failed': 'Snapshot policy failed',
};

export function NotificationsTab() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const q = useQuery({
    queryKey: ['notifications'],
    queryFn: () => apiGet<Reply>('/api/settings/notifications'),
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
      qc.invalidateQueries({ queryKey: ['notifications'] });
    },
    onError: (e: Error) => toast("error", 'Save failed: ' + e.message),
  });

  const test = useMutation({
    mutationFn: (channelID: string) =>
      apiPost('/api/settings/notifications/test', { channel_id: channelID }),
    onSuccess: () => {
      toast("success", 'Test notification sent');
      qc.invalidateQueries({ queryKey: ['notifications'] });
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
  const addChannel = (kind: 'webhook' | 'email') => {
    update({
      channels: [
        ...form.channels,
        { id: '', name: kind === 'webhook' ? 'New webhook' : 'New email', kind, enabled: true },
      ],
    });
  };
  const removeChannel = (idx: number) => {
    update({ channels: form.channels.filter((_, i) => i !== idx) });
  };
  const allTypes = q.data!.all_types;

  return (
    <div style={{ display: 'grid', gap: 16 }}>
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
              {ch.kind === 'webhook' ? (
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
              ) : (
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
