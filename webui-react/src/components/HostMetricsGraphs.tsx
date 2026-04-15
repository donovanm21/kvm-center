import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';

/**
 * Glanceable in-UI sparklines for a single hypervisor. Last hour only;
 * Grafana + Influx is the advanced surface for longer windows, per-VM
 * breakdowns, and anything cross-host. The ring buffer is memory-only
 * on the server, so after a restart the graphs start empty and fill in
 * over the next hour.
 */

interface HostMetricSample {
  t: string;
  cpu: number;
  mem: number;
  storage: number;
  vms: number;
}

interface HostMetricsResponse {
  host_id: string;
  window_cap: number;
  interval_sec: number;
  samples: HostMetricSample[];
}

export function HostMetricsGraphs({ hostID }: { hostID: string }) {
  const { data, isLoading } = useQuery<HostMetricsResponse>({
    queryKey: ['host-metrics', hostID],
    queryFn: () => apiGet<HostMetricsResponse>(`/api/hosts/${encodeURIComponent(hostID)}/metrics`),
    refetchInterval: 30_000,
    staleTime: 15_000,
  });

  const samples = data?.samples ?? [];
  const empty = samples.length < 2;

  return (
    <div>
      <div style={sectionHeader}>
        <span>Last hour</span>
        <a href="/settings/metrics" style={linkStyle}>Full metrics in Grafana →</a>
      </div>
      <div className="panel" style={{ padding: 18, display: 'grid', gap: 14 }}>
        {empty ? (
          <div style={emptyStyle}>
            {isLoading ? 'Loading metrics…' : 'Waiting for samples — graph fills in over the next hour.'}
          </div>
        ) : (
          <>
            <Sparkline label="CPU"     unit="%" samples={samples} pick={(s) => s.cpu}     stroke="#E3571E" />
            <Sparkline label="Memory"  unit="%" samples={samples} pick={(s) => s.mem}     stroke="#58A6FF" />
            <Sparkline label="Storage" unit="%" samples={samples} pick={(s) => s.storage} stroke="#3FB950" />
          </>
        )}
      </div>
    </div>
  );
}

/* ── Sparkline ─────────────────────────────────────────────── */

interface SparklineProps {
  label: string;
  unit: string;
  samples: HostMetricSample[];
  pick: (s: HostMetricSample) => number;
  stroke: string;
}

function Sparkline({ label, unit, samples, pick, stroke }: SparklineProps) {
  const W = 800;
  const H = 70;
  const PAD = 4;
  const innerW = W - PAD * 2;
  const innerH = H - PAD * 2;

  const values = samples.map(pick);
  const latest = values[values.length - 1] ?? 0;
  const peak = values.length ? Math.max(...values) : 0;

  // Fixed 0–100 scale so CPU/Mem/Storage share a visual baseline and
  // the 65/85 threshold gridlines are meaningful. Values above 100 are
  // clamped for display but shown in the tooltip numerically.
  const scaleY = (v: number) => PAD + innerH - (Math.min(100, Math.max(0, v)) / 100) * innerH;
  const scaleX = (i: number) =>
    PAD + (values.length <= 1 ? 0 : (i / (values.length - 1)) * innerW);

  const linePath = values
    .map((v, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(i).toFixed(1)} ${scaleY(v).toFixed(1)}`)
    .join(' ');

  const areaPath =
    linePath &&
    `${linePath} L ${scaleX(values.length - 1).toFixed(1)} ${(PAD + innerH).toFixed(1)} L ${PAD} ${(PAD + innerH).toFixed(1)} Z`;

  const gridY65 = scaleY(65);
  const gridY85 = scaleY(85);

  return (
    <div>
      <div style={rowHeader}>
        <span style={labelStyle}>{label}</span>
        <span style={valueStyle}>
          <span style={{ color: toneFor(latest) }}>{latest.toFixed(1)}{unit}</span>
          <span style={peakStyle}> · peak {peak.toFixed(1)}{unit}</span>
        </span>
      </div>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        style={{ width: '100%', height: H, display: 'block' }}
      >
        {/* thresholds */}
        <line x1={PAD} x2={W - PAD} y1={gridY65} y2={gridY65} stroke="#D29922" strokeWidth={0.5} strokeDasharray="3 4" opacity={0.35} />
        <line x1={PAD} x2={W - PAD} y1={gridY85} y2={gridY85} stroke="#F85149" strokeWidth={0.5} strokeDasharray="3 4" opacity={0.4} />
        {/* area fill */}
        {areaPath && <path d={areaPath} fill={stroke} opacity={0.12} />}
        {/* line */}
        {linePath && <path d={linePath} fill="none" stroke={stroke} strokeWidth={1.4} strokeLinejoin="round" strokeLinecap="round" />}
        {/* latest dot */}
        {values.length > 0 && (
          <circle
            cx={scaleX(values.length - 1)}
            cy={scaleY(latest)}
            r={2.4}
            fill={stroke}
          />
        )}
      </svg>
    </div>
  );
}

function toneFor(v: number): string {
  if (v >= 85) return '#F85149';
  if (v >= 65) return '#D29922';
  return 'var(--text)';
}

/* ── Styles ────────────────────────────────────────────────── */

const sectionHeader: React.CSSProperties = {
  display: 'flex',
  alignItems: 'baseline',
  justifyContent: 'space-between',
  fontSize: 11,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  color: 'var(--text-faded)',
  padding: '0 4px 6px',
  borderBottom: '1px solid var(--border-subtle)',
  marginBottom: 10,
};

const linkStyle: React.CSSProperties = {
  fontSize: 11,
  fontWeight: 500,
  textTransform: 'none',
  letterSpacing: 0,
  color: 'var(--accent-hover)',
  textDecoration: 'none',
};

const emptyStyle: React.CSSProperties = {
  padding: 20,
  textAlign: 'center',
  fontSize: 12,
  color: 'var(--text-muted)',
};

const rowHeader: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'baseline',
  marginBottom: 4,
  fontSize: 11,
};

const labelStyle: React.CSSProperties = {
  color: 'var(--text-muted)',
  textTransform: 'uppercase',
  letterSpacing: '0.06em',
  fontWeight: 600,
};

const valueStyle: React.CSSProperties = {
  fontFamily: "'SF Mono', monospace",
  fontVariantNumeric: 'tabular-nums',
};

const peakStyle: React.CSSProperties = {
  color: 'var(--text-faded)',
};
