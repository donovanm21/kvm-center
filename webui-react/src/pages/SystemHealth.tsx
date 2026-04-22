import { useQuery } from '@tanstack/react-query';
import type { CSSProperties } from 'react';
import { apiGet } from '@/api/client';

// System Health dashboard (v5.33.1). Modelled after the Checkmk
// Kubernetes Cluster page style — top-line status tiles, gauges,
// sparkline time-series, breakdown tables, problems feed. All data
// comes from one call to /api/admin/system-health (cached 15s
// server-side); the SPA polls every 15s.
//
// No charting library — simple SVG renders for gauges + area charts
// keeps the bundle small and matches the rest of the app's
// hand-rolled inline SVG approach.

type Status = 'green' | 'amber' | 'red';

interface Subsystem {
  status: Status;
  headline: string;
  detail?: Record<string, string>;
  measured_at: string;
}

interface HostBrief {
  id: string;
  status: string;
  agent_version?: string;
  cpu_pct: number;
  mem_pct: number;
  vm_count: number;
  storage_pct: number;
  last_seen: number;
  outdated?: boolean;
  maintenance?: boolean;
}

interface Problem {
  severity: 'amber' | 'red';
  source: string;
  resource: string;
  summary: string;
}

interface Aggregates {
  fleet_cpu_pct: number;
  fleet_mem_pct: number;
  fleet_vm_count: number;
  fleet_vm_running: number;
  fleet_cpu_cores: number;
  fleet_mem_total_gb: number;
  migrations_today: number;
  snapshot_blocked: number;
  series: { t: number[]; cpu_pct: number[]; mem_pct: number[]; vms: number[] };
}

interface Health {
  overall: Status;
  generated_at: string;
  control_plane: Subsystem;
  database: Subsystem;
  tls: Subsystem;
  hypervisors: Subsystem;
  guest_tools: Subsystem;
  migrations: Subsystem;
  snapshots: Subsystem;
  command_log: Subsystem;
  aggregates: Aggregates;
  host_breakdown: HostBrief[];
  problems: Problem[];
}

const C: Record<Status, string> = {
  green: '#3FB950',
  amber: '#D29922',
  red: '#F85149',
};

export function SystemHealth() {
  const q = useQuery<Health>({
    queryKey: ['system-health'],
    queryFn: () => apiGet('/api/admin/system-health'),
    refetchInterval: 15_000,
    refetchOnWindowFocus: true,
  });

  if (q.isLoading) return <p style={{ padding: 16 }}>Loading system health…</p>;
  if (q.error) return (
    <div style={{ padding: 16 }}>
      <h2>System Health</h2>
      <p style={{ color: C.red }}>Failed to fetch: {(q.error as Error).message}</p>
    </div>
  );
  if (!q.data) return null;

  const h = q.data;
  const a = h.aggregates;

  return (
    <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <header style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
        <div>
          <h2 style={{ margin: 0 }}>System Health</h2>
          <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>
            Fleet overview — last checked {new Date(h.generated_at).toLocaleTimeString()} (polls every 15 s)
          </div>
        </div>
        <StatusPill status={h.overall} large />
      </header>

      {/* Row 1: Big status tiles */}
      <div style={bigTileRow}>
        <BigTile label="Overall"      sub={h.overall}            status={h.overall} />
        <BigTile label="Control plane" sub={h.control_plane.status} status={h.control_plane.status} />
        <BigTile label="Database"     sub={h.database.status}    status={h.database.status} />
        <BigTile label="TLS & CA"     sub={h.tls.status}         status={h.tls.status} />
        <BigTile label="Hypervisors"  sub={h.hypervisors.status} status={h.hypervisors.status} />
      </div>

      {/* Row 2: Gauges + line charts */}
      <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 12 }}>
        <div style={panelStyle}>
          <div style={panelTitleStyle}>CPU utilization: Fleet</div>
          <Gauge value={a.fleet_cpu_pct} />
          <div style={panelFootnote}>{a.fleet_cpu_cores} cores total</div>
        </div>
        <div style={panelStyle}>
          <div style={panelTitleStyle}>CPU utilization over the last hour</div>
          <AreaChart
            series={a.series.t.map((t, i) => ({ t, v: a.series.cpu_pct[i] ?? 0 }))}
            color={C.green}
            unit="%"
            height={160}
          />
        </div>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 12 }}>
        <div style={panelStyle}>
          <div style={panelTitleStyle}>Memory utilization: Fleet</div>
          <Gauge value={a.fleet_mem_pct} />
          <div style={panelFootnote}>{a.fleet_mem_total_gb.toFixed(1)} GB total</div>
        </div>
        <div style={panelStyle}>
          <div style={panelTitleStyle}>Memory utilization over the last hour</div>
          <AreaChart
            series={a.series.t.map((t, i) => ({ t, v: a.series.mem_pct[i] ?? 0 }))}
            color="#B37FEB"
            unit="%"
            height={160}
          />
        </div>
      </div>

      {/* Row 3: VM counts + subsystem cards grid */}
      <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 12 }}>
        <div style={panelStyle}>
          <div style={panelTitleStyle}>Running VMs: Fleet</div>
          <BigNumber number={a.fleet_vm_running} sub={`of ${a.fleet_vm_count} total`} color="#4B8FF7" />
          <VMTrendSparkline series={a.series.t.map((t, i) => ({ t, v: a.series.vms[i] ?? 0 }))} />
          <div style={panelFootnote}>
            {a.migrations_today} migration{a.migrations_today === 1 ? '' : 's'} started in the last 24h
            {a.snapshot_blocked > 0 && <> · {a.snapshot_blocked} snapshot-blocked VM{a.snapshot_blocked === 1 ? '' : 's'}</>}
          </div>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))', gap: 12 }}>
          <SubsystemCard name="Guest kvm-tools" sub={h.guest_tools} />
          <SubsystemCard name="Migrations"       sub={h.migrations}  />
          <SubsystemCard name="Snapshots"        sub={h.snapshots}   />
          <SubsystemCard name="Command log"      sub={h.command_log} />
        </div>
      </div>

      {/* Row 4: Host breakdown table */}
      <div style={panelStyle}>
        <div style={panelTitleStyle}>Hypervisors</div>
        {h.host_breakdown.length === 0 ? (
          <p style={{ color: 'var(--text-muted)' }}>No hypervisors enrolled.</p>
        ) : (
          <table className="data-table" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th>Host</th>
                <th>Status</th>
                <th>Agent</th>
                <th>CPU</th>
                <th>Memory</th>
                <th>Storage</th>
                <th>VMs</th>
              </tr>
            </thead>
            <tbody>
              {h.host_breakdown.map((hb) => (
                <tr key={hb.id}>
                  <td data-label="Host">{hb.id}{hb.maintenance && <span style={{ marginLeft: 6, color: C.amber }}>⚑ maint</span>}</td>
                  <td data-label="Status"><StatusChip label={hb.status} ok={hb.status === 'online'} /></td>
                  <td data-label="Agent">
                    {hb.agent_version || <span style={{ color: 'var(--text-muted)' }}>—</span>}
                    {hb.outdated && <span style={{ marginLeft: 6, color: C.amber }}>⚠ outdated</span>}
                  </td>
                  <td data-label="CPU"><Bar pct={hb.cpu_pct} /></td>
                  <td data-label="Memory"><Bar pct={hb.mem_pct} /></td>
                  <td data-label="Storage"><Bar pct={hb.storage_pct} /></td>
                  <td data-label="VMs">{hb.vm_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Row 5: Problems feed (hidden when empty) */}
      {h.problems.length > 0 && (
        <div style={panelStyle}>
          <div style={panelTitleStyle}>Cluster problems ({h.problems.length})</div>
          <table className="data-table" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th>Severity</th>
                <th>Source</th>
                <th>Resource</th>
                <th>Summary</th>
              </tr>
            </thead>
            <tbody>
              {h.problems.map((p, i) => (
                <tr key={i}>
                  <td data-label="Severity">
                    <span style={{
                      padding: '2px 8px', borderRadius: 3,
                      background: C[p.severity], color: '#000',
                      fontWeight: 700, fontSize: 11, textTransform: 'uppercase', letterSpacing: 0.4,
                    }}>{p.severity === 'red' ? 'CRIT' : 'WARN'}</span>
                  </td>
                  <td data-label="Source">{p.source}</td>
                  <td data-label="Resource" style={{ fontFamily: 'var(--mono, monospace)', fontSize: 12 }}>{p.resource}</td>
                  <td data-label="Summary">{p.summary}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/* ── Building blocks ──────────────────────────────────────── */

const bigTileRow: CSSProperties = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
  gap: 12,
};

const panelStyle: CSSProperties = {
  border: '1px solid var(--border)',
  background: 'var(--surface-1)',
  borderRadius: 6,
  padding: '14px 16px',
};
const panelTitleStyle: CSSProperties = {
  fontSize: 12,
  color: 'var(--text-muted)',
  textTransform: 'uppercase',
  letterSpacing: 0.08,
  marginBottom: 10,
  textAlign: 'center',
};
const panelFootnote: CSSProperties = {
  fontSize: 11, color: 'var(--text-muted)', textAlign: 'center', marginTop: 6,
};

function StatusPill({ status, large = false }: { status: Status; large?: boolean }) {
  const s = large ? 18 : 14;
  return (
    <span
      style={{
        padding: large ? '6px 14px' : '2px 10px',
        borderRadius: 4,
        background: C[status],
        color: '#000',
        fontWeight: 700,
        fontSize: s - 2,
        textTransform: 'uppercase',
        letterSpacing: 0.6,
      }}
    >
      {status}
    </span>
  );
}

function StatusChip({ label, ok }: { label: string; ok: boolean }) {
  const c = ok ? C.green : C.red;
  return (
    <span style={{
      padding: '2px 8px', borderRadius: 3,
      background: c, color: '#000',
      fontWeight: 700, fontSize: 11, textTransform: 'uppercase', letterSpacing: 0.4,
    }}>{label}</span>
  );
}

function BigTile({ label, sub, status }: { label: string; sub: string; status: Status }) {
  return (
    <div style={{
      border: '1px solid var(--border)',
      borderRadius: 6,
      background: C[status],
      color: '#000',
      padding: '18px 12px',
      textAlign: 'center',
      minHeight: 120,
      display: 'flex', flexDirection: 'column', justifyContent: 'center', gap: 6,
    }}>
      <div style={{ fontSize: 12, fontWeight: 600, opacity: 0.75, textTransform: 'uppercase', letterSpacing: 0.08 }}>{label}</div>
      <div style={{ fontSize: 30, fontWeight: 800, textTransform: 'uppercase', letterSpacing: 0.08 }}>
        {statusLabel(sub)}
      </div>
    </div>
  );
}

function statusLabel(s: string) {
  if (s === 'green') return 'OK';
  if (s === 'amber') return 'WARN';
  if (s === 'red') return 'CRIT';
  return s.toUpperCase();
}

/* ── Gauge ─────────────────────────────────────────────────── */
// Semicircle gauge. Top-half arc from 9 o'clock (180°) → 12 o'clock
// (90°) → 3 o'clock (0°). SVG arc sweep-flag=0 means "counterclockwise
// on screen" which — because SVG y-axis is flipped — traces the TOP
// half of the circle (the half where y < cy). v5.33.1's initial
// version used sweep-flag=1 which drew the bottom half; at low
// percentages like memory=8% it showed a tiny sliver in the
// wrong place. Fixed in v5.33.2.
function Gauge({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(100, value));
  const r = 72;
  const cx = 100;
  const cy = 100;
  const start = polar(cx, cy, r, 180);
  const end = polar(cx, cy, r, 180 - (180 * pct) / 100);
  const fullEnd = polar(cx, cy, r, 0);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
      <svg viewBox="0 0 200 120" width="100%" style={{ maxWidth: 260 }}>
        {/* Background track */}
        <path
          d={`M ${start.x} ${start.y} A ${r} ${r} 0 0 0 ${fullEnd.x} ${fullEnd.y}`}
          stroke="var(--surface-2)"
          strokeWidth={14}
          fill="none"
          strokeLinecap="round"
        />
        {/* Filled portion */}
        {pct > 0 && (
          <path
            d={`M ${start.x} ${start.y} A ${r} ${r} 0 0 0 ${end.x} ${end.y}`}
            stroke="#FFFFFF"
            strokeWidth={14}
            fill="none"
            strokeLinecap="round"
          />
        )}
        <text
          x={cx}
          y={cy - 8}
          textAnchor="middle"
          fontSize={28}
          fontWeight={700}
          fill="var(--text-strong)"
        >
          {pct.toFixed(2)}
          <tspan fontSize={16}>%</tspan>
        </text>
        <text x={cx - r - 4} y={cy + 20} fontSize={10} fill="var(--text-muted)">
          0%
        </text>
        <text x={cx + r + 4} y={cy + 20} fontSize={10} fill="var(--text-muted)" textAnchor="end">
          100%
        </text>
      </svg>
    </div>
  );
}

function polar(cx: number, cy: number, r: number, angleDeg: number) {
  const a = (angleDeg * Math.PI) / 180;
  return { x: cx + r * Math.cos(a), y: cy - r * Math.sin(a) };
}

/* ── Area chart ────────────────────────────────────────────── */
interface Point { t: number; v: number }
function AreaChart({ series, color, unit, height }: { series: Point[]; color: string; unit: string; height: number }) {
  if (series.length < 2) {
    return (
      <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-muted)', fontSize: 12 }}>
        Not enough samples yet — waiting on telemetry to accumulate.
      </div>
    );
  }
  const w = 600;
  const h = height;
  const pad = { l: 30, r: 6, t: 6, b: 16 };
  const innerW = w - pad.l - pad.r;
  const innerH = h - pad.t - pad.b;
  const minT = series[0].t;
  const maxT = series[series.length - 1].t;
  const maxV = Math.max(100, ...series.map((p) => p.v));
  const x = (t: number) => pad.l + (innerW * (t - minT)) / Math.max(1, maxT - minT);
  const y = (v: number) => pad.t + innerH - (innerH * v) / maxV;
  const poly = series.map((p) => `${x(p.t)},${y(p.v)}`).join(' ');
  const area = `${pad.l},${pad.t + innerH} ${poly} ${x(series[series.length - 1].t)},${pad.t + innerH}`;
  // Y-axis gridlines at 0 / 25 / 50 / 75 / 100.
  const grid = [0, 25, 50, 75, 100];
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" preserveAspectRatio="none" style={{ display: 'block' }}>
      {grid.map((g) => (
        <g key={g}>
          <line x1={pad.l} x2={w - pad.r} y1={y(g)} y2={y(g)} stroke="var(--border)" strokeDasharray="2 3" />
          <text x={pad.l - 4} y={y(g) + 3} fontSize={9} fill="var(--text-muted)" textAnchor="end">{g}{unit}</text>
        </g>
      ))}
      <polyline points={area} fill={color} fillOpacity={0.25} stroke="none" />
      <polyline points={poly} fill="none" stroke={color} strokeWidth={2} />
      <text x={pad.l} y={h - 2} fontSize={9} fill="var(--text-muted)">{new Date(minT * 1000).toLocaleTimeString()}</text>
      <text x={w - pad.r} y={h - 2} fontSize={9} fill="var(--text-muted)" textAnchor="end">{new Date(maxT * 1000).toLocaleTimeString()}</text>
    </svg>
  );
}

/* ── VM count mini-chart (0–max scaled) ────────────────────── */
function VMTrendSparkline({ series }: { series: Point[] }) {
  if (series.length < 2) return null;
  const w = 260;
  const h = 80;
  const maxV = Math.max(1, ...series.map((p) => p.v));
  const minT = series[0].t;
  const maxT = series[series.length - 1].t;
  const x = (t: number) => (w * (t - minT)) / Math.max(1, maxT - minT);
  const y = (v: number) => h - (h * v) / maxV;
  const poly = series.map((p) => `${x(p.t)},${y(p.v)}`).join(' ');
  const area = `0,${h} ${poly} ${w},${h}`;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" preserveAspectRatio="none" style={{ marginTop: 4 }}>
      <polyline points={area} fill="#4B8FF7" fillOpacity={0.2} stroke="none" />
      <polyline points={poly} fill="none" stroke="#4B8FF7" strokeWidth={1.5} />
    </svg>
  );
}

/* ── Big number card ───────────────────────────────────────── */
function BigNumber({ number, sub, color }: { number: number; sub: string; color: string }) {
  return (
    <div style={{ textAlign: 'center', padding: '8px 0' }}>
      <div style={{ fontSize: 48, fontWeight: 800, color }}>{number}</div>
      <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{sub}</div>
    </div>
  );
}

/* ── Inline bar (hosts table) ──────────────────────────────── */
function Bar({ pct }: { pct: number }) {
  const clamped = Math.max(0, Math.min(100, pct));
  const color = clamped >= 90 ? C.red : clamped >= 70 ? C.amber : C.green;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 140 }}>
      <div style={{ flex: 1, background: 'var(--surface-2)', height: 8, borderRadius: 4, overflow: 'hidden' }}>
        <div style={{ width: `${clamped}%`, height: '100%', background: color }} />
      </div>
      <span style={{ fontSize: 12, color: 'var(--text-muted)', minWidth: 48, textAlign: 'right' }}>{clamped.toFixed(1)}%</span>
    </div>
  );
}

/* ── Subsystem card (secondary subsystems below the fold) ── */
function SubsystemCard({ name, sub }: { name: string; sub: Subsystem }) {
  return (
    <div style={{
      border: '1px solid var(--border)',
      borderLeft: `3px solid ${C[sub.status]}`,
      borderRadius: 6,
      padding: '10px 12px',
      background: 'var(--surface-1)',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
        <span style={{ fontWeight: 600 }}>{name}</span>
        <StatusPill status={sub.status} />
      </div>
      <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4 }}>{sub.headline}</div>
    </div>
  );
}
