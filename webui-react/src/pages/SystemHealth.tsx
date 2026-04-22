import { useQuery } from '@tanstack/react-query';
import { apiGet } from '@/api/client';

// System health page (v5.33.0, P1.1). Polls /api/admin/system-health
// every 15 seconds — same cache TTL as the server-side cache so we
// don't double up work. Renders cards for each subsystem plus a
// rollup banner at the top. Admin-only: the route requires
// user.manage at the Layout nav level.

type Status = 'green' | 'amber' | 'red';

interface Subsystem {
  status: Status;
  headline: string;
  detail?: Record<string, string>;
  measured_at: string;
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
}

const STATUS_COLOR: Record<Status, string> = {
  green: 'var(--accent-green, #3fb950)',
  amber: 'var(--accent-amber, #d29922)',
  red: 'var(--accent-red, #f85149)',
};

const STATUS_GLYPH: Record<Status, string> = {
  green: '✓',
  amber: '!',
  red: '✕',
};

export function SystemHealth() {
  const q = useQuery<Health>({
    queryKey: ['system-health'],
    queryFn: () => apiGet('/api/admin/system-health'),
    refetchInterval: 15_000,
    refetchOnWindowFocus: true,
  });

  if (q.isLoading) {
    return <p style={{ padding: 16 }}>Loading system health…</p>;
  }
  if (q.error) {
    return (
      <div style={{ padding: 16 }}>
        <h2>System health</h2>
        <p style={{ color: STATUS_COLOR.red }}>
          Failed to fetch: {(q.error as Error).message}
        </p>
      </div>
    );
  }
  if (!q.data) return null;
  const h = q.data;

  const rollupMsg = h.overall === 'green'
    ? 'All subsystems healthy'
    : h.overall === 'amber'
    ? 'Degraded — one or more subsystems need attention'
    : 'Critical — operator action required';

  const subsystems: Array<[string, Subsystem]> = [
    ['Control plane', h.control_plane],
    ['Database', h.database],
    ['TLS & certificates', h.tls],
    ['Hypervisors', h.hypervisors],
    ['Guest kvm-tools', h.guest_tools],
    ['Migrations', h.migrations],
    ['Snapshots', h.snapshots],
    ['Command log', h.command_log],
  ];

  return (
    <div style={{ padding: 16 }}>
      <h2 style={{ marginBottom: 4 }}>System health</h2>
      <p style={{ marginTop: 0, color: 'var(--text-muted)', fontSize: 13 }}>
        Last checked {new Date(h.generated_at).toLocaleTimeString()} — polls every 15 s
      </p>

      <RollupBanner status={h.overall} message={rollupMsg} />

      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))',
          gap: 12,
          marginTop: 16,
        }}
      >
        {subsystems.map(([name, s]) => (
          <SubsystemCard key={name} name={name} sub={s} />
        ))}
      </div>
    </div>
  );
}

function RollupBanner({ status, message }: { status: Status; message: string }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '12px 16px',
        marginTop: 12,
        border: `1px solid ${STATUS_COLOR[status]}`,
        background: `color-mix(in srgb, ${STATUS_COLOR[status]} 10%, transparent)`,
        borderRadius: 6,
      }}
    >
      <span
        aria-hidden
        style={{
          width: 32,
          height: 32,
          borderRadius: '50%',
          background: STATUS_COLOR[status],
          color: '#000',
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          fontSize: 18,
          fontWeight: 700,
        }}
      >
        {STATUS_GLYPH[status]}
      </span>
      <div>
        <div style={{ fontSize: 16, fontWeight: 600, textTransform: 'uppercase' }}>{status}</div>
        <div style={{ fontSize: 13 }}>{message}</div>
      </div>
    </div>
  );
}

function SubsystemCard({ name, sub }: { name: string; sub: Subsystem }) {
  const color = STATUS_COLOR[sub.status];
  return (
    <div
      style={{
        border: '1px solid var(--border)',
        borderLeft: `3px solid ${color}`,
        borderRadius: 6,
        padding: '12px 14px',
        background: 'var(--surface-1)',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 6 }}>
        <span style={{ fontWeight: 600 }}>{name}</span>
        <span
          aria-label={`Status: ${sub.status}`}
          style={{
            fontSize: 11,
            padding: '2px 8px',
            borderRadius: 10,
            background: color,
            color: '#000',
            textTransform: 'uppercase',
            letterSpacing: 0.4,
            fontWeight: 700,
          }}
        >
          {STATUS_GLYPH[sub.status]} {sub.status}
        </span>
      </div>
      <div style={{ fontSize: 13, color: 'var(--text-muted)' }}>{sub.headline}</div>
      {sub.detail && Object.keys(sub.detail).length > 0 && (
        <DetailGrid detail={sub.detail} />
      )}
    </div>
  );
}

function DetailGrid({ detail }: { detail: Record<string, string> }) {
  const keys = Object.keys(detail).sort();
  return (
    <dl
      style={{
        display: 'grid',
        gridTemplateColumns: 'auto 1fr',
        columnGap: 10,
        rowGap: 2,
        fontSize: 12,
        marginTop: 8,
        marginBottom: 0,
      }}
    >
      {keys.map((k) => (
        <div key={k} style={{ display: 'contents' }}>
          <dt style={{ color: 'var(--text-muted)' }}>{k}</dt>
          <dd style={{ margin: 0, wordBreak: 'break-all' }}>{detail[k]}</dd>
        </div>
      ))}
    </dl>
  );
}
