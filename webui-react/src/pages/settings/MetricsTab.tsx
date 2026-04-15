import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost, apiPut } from '@/api/client';
import { useToast } from '@/hooks/useToast';

interface MetricsConfig {
  enabled: boolean;
  url?: string;
  org?: string;
  bucket?: string;
  token?: string;
  measurement_prefix?: string;
  batch_size?: number;
  flush_seconds?: number;
  last_write_at?: string;
  last_write_error?: string;
  last_write_count?: number;
}

export function MetricsTab() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const q = useQuery({
    queryKey: ['metrics-settings'],
    queryFn: () => apiGet<MetricsConfig>('/api/settings/metrics'),
  });
  const [form, setForm] = useState<MetricsConfig | null>(null);
  useEffect(() => { if (q.data) setForm(q.data); }, [q.data]);

  const save = useMutation({
    mutationFn: (cfg: MetricsConfig) => apiPut('/api/settings/metrics', cfg),
    onSuccess: () => {
      toast('success', 'Metrics settings saved');
      qc.invalidateQueries({ queryKey: ['metrics-settings'] });
    },
    onError: (e: Error) => toast('error', 'Save failed: ' + e.message),
  });
  const test = useMutation({
    mutationFn: () => apiPost('/api/settings/metrics/test'),
    onSuccess: () => {
      toast('success', 'Test sample accepted by InfluxDB');
      qc.invalidateQueries({ queryKey: ['metrics-settings'] });
    },
    onError: (e: Error) => toast('error', 'Test failed: ' + e.message),
  });

  if (q.isLoading || !form) return <div style={muted}>Loading…</div>;
  if (q.error) return <div className="tone-pill danger">Failed to load: {(q.error as Error).message}</div>;

  const update = (patch: Partial<MetricsConfig>) => setForm({ ...form, ...patch });

  return (
    <div style={{ display: 'grid', gap: 14 }}>
      <div className="panel">
        <h2 style={{ margin: 0, marginBottom: 4 }}>InfluxDB v2 telemetry sink</h2>
        <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: '0 0 12px 0' }}>
          Ship host + VM telemetry samples to an external InfluxDB bucket for long-term retention and Grafana dashboards.
          Disabled by default — fleet doesn't depend on Influx health.
        </p>

        <label style={checkboxRow}>
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => update({ enabled: e.target.checked })}
          />
          <span><strong>Enable metrics</strong></span>
        </label>

        <div style={grid}>
          <Field label="URL">
            <input type="url" value={form.url || ''} onChange={(e) => update({ url: e.target.value })}
              placeholder="https://influx.example.com:8086" style={inputStyle} />
          </Field>
          <Field label="Organisation">
            <input type="text" value={form.org || ''} onChange={(e) => update({ org: e.target.value })}
              placeholder="home" style={inputStyle} />
          </Field>
          <Field label="Bucket">
            <input type="text" value={form.bucket || ''} onChange={(e) => update({ bucket: e.target.value })}
              placeholder="kvm-center" style={inputStyle} />
          </Field>
          <Field label="Token">
            <input type="password" value={form.token || ''} onChange={(e) => update({ token: e.target.value })}
              placeholder="influx API token" style={inputStyle} />
          </Field>
          <Field label="Measurement prefix">
            <input type="text" value={form.measurement_prefix || ''} onChange={(e) => update({ measurement_prefix: e.target.value })}
              placeholder="kvm_center_" style={inputStyle} />
          </Field>
          <Field label="Batch size">
            <input type="number" value={form.batch_size ?? 100} onChange={(e) => update({ batch_size: parseInt(e.target.value || '0', 10) })} style={inputStyle} />
          </Field>
          <Field label="Flush seconds">
            <input type="number" value={form.flush_seconds ?? 10} onChange={(e) => update({ flush_seconds: parseInt(e.target.value || '0', 10) })} style={inputStyle} />
          </Field>
        </div>

        {(form.last_write_at || form.last_write_error) && (
          <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text-faded)' }}>
            Last write: {form.last_write_at || '—'}
            {form.last_write_count ? ` · ${form.last_write_count} samples` : ''}
            {form.last_write_error && (
              <div className="tone-pill danger" style={{ marginTop: 4, display: 'inline-block' }}>
                {form.last_write_error}
              </div>
            )}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 14, justifyContent: 'flex-end' }}>
          <button className="btn-secondary-sm" disabled={!form.enabled || test.isPending} onClick={() => test.mutate()}>
            {test.isPending ? 'Testing…' : 'Send test sample'}
          </button>
          <button className="btn-primary-sm" disabled={save.isPending} onClick={() => save.mutate(form)}>
            {save.isPending ? 'Saving…' : 'Save settings'}
          </button>
        </div>
      </div>

      <div className="panel" style={{ fontSize: 12, color: 'var(--text-muted)' }}>
        <h3 style={{ margin: 0, color: 'var(--text-strong)' }}>What's written</h3>
        <ul style={{ paddingLeft: 20, lineHeight: 1.6 }}>
          <li><code>{(form.measurement_prefix || 'kvm_center_')}host</code> — per-host sample every telemetry tick (cpu_usage, mem_usage, mem_total_mb, mem_free_mb, vm_count).</li>
          <li><code>{(form.measurement_prefix || 'kvm_center_')}vm</code> — per-VM sample (cpu_usage, mem_usage_mb, state_code, snapshot_count) tagged with host_id, vm_uuid, vm_name, state.</li>
        </ul>
        <p style={{ margin: '8px 0 0 0', display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <a
            href="/api/grafana/dashboard.json"
            className="btn-secondary-sm"
            style={{ textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 6 }}
            download
          >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
              <path d="M.5 9.9a.5.5 0 0 1 .5.5v2.5a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-2.5a.5.5 0 0 1 1 0v2.5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2v-2.5a.5.5 0 0 1 .5-.5z"/>
              <path d="M7.646 11.854a.5.5 0 0 0 .708 0l3-3a.5.5 0 0 0-.708-.708L8.5 10.293V1.5a.5.5 0 0 0-1 0v8.793L5.354 8.146a.5.5 0 1 0-.708.708l3 3z"/>
            </svg>
            Download Grafana dashboard
          </a>
          <span style={{ fontSize: 11, color: 'var(--text-faded)' }}>
            Import via Grafana → Dashboards → New → Import → Upload JSON.
          </span>
        </p>
      </div>
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

const muted: React.CSSProperties = { color: 'var(--text-muted)', fontSize: 12 };
const checkboxRow: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, fontSize: 13, color: 'var(--text-body)',
};
const grid: React.CSSProperties = { display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 };
const inputStyle: React.CSSProperties = {
  padding: '6px 10px', fontSize: 13,
  background: 'var(--bg-input)', border: '1px solid var(--border)',
  borderRadius: 4, color: 'var(--text-body)', fontFamily: 'inherit',
};
