import { useHosts, useVMs, useStorage } from '@/hooks/useInventory';

export function Dashboard() {
  const hosts = useHosts();
  const vms = useVMs();
  const storage = useStorage();

  const totalHosts = hosts.data?.length ?? 0;
  const onlineHosts = hosts.data?.filter((h) => h.status === 'online').length ?? 0;
  const totalVMs = vms.data?.length ?? 0;
  const runningVMs = vms.data?.filter((v) => v.state === 'running').length ?? 0;
  const totalCapacity = (storage.data ?? []).reduce((s, p) => s + p.capacity_gb, 0);
  const totalUsed = (storage.data ?? []).reduce((s, p) => s + p.allocation_gb, 0);

  return (
    <>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
        <Stat label="Hosts" value={totalHosts} sub={`${onlineHosts} online`} />
        <Stat label="Virtual machines" value={totalVMs} sub={`${runningVMs} running`} />
        <Stat
          label="Storage"
          value={totalCapacity ? `${totalUsed.toFixed(1)} / ${totalCapacity.toFixed(0)} GB` : '—'}
          sub={totalCapacity ? `${Math.round((totalUsed / totalCapacity) * 100)}% used` : 'no pools reported'}
        />
      </div>

      <div className="panel" style={{ marginTop: 16 }}>
        <h2>Hypervisors</h2>
        {hosts.isLoading && <div style={{ color: 'var(--text-muted)' }}>Loading…</div>}
        {hosts.error && <div style={{ color: '#F85149' }}>Failed to load hosts: {(hosts.error as Error).message}</div>}
        {hosts.data && hosts.data.length === 0 && (
          <div style={{ color: 'var(--text-muted)' }}>No hypervisors have reported yet.</div>
        )}
        {hosts.data && hosts.data.length > 0 && (
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr>
                <th style={th}>Host</th>
                <th style={th}>IP</th>
                <th style={th}>Status</th>
                <th style={th}>VMs</th>
                <th style={th}>CPU</th>
                <th style={th}>Memory</th>
              </tr>
            </thead>
            <tbody>
              {hosts.data.map((h) => (
                <tr key={h.id}>
                  <td style={td}><strong style={{ color: 'var(--text-strong)' }}>{h.id}</strong></td>
                  <td style={td}>{h.ip}</td>
                  <td style={td}>
                    <span style={{ color: h.status === 'online' ? '#3FB950' : '#8B949E' }}>● {h.status}</span>
                  </td>
                  <td style={td}>{h.vm_count}</td>
                  <td style={td}>{h.cpu_usage.toFixed(1)}% of {h.cpu_count}</td>
                  <td style={td}>{((h.mem_total_mb - h.mem_free_mb) / 1024).toFixed(1)} / {(h.mem_total_mb / 1024).toFixed(0)} GB</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}

const th: React.CSSProperties = {
  textAlign: 'left',
  fontSize: 10,
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  color: 'var(--text-faded)',
  padding: '8px 10px',
  borderBottom: '1px solid var(--border)',
};
const td: React.CSSProperties = {
  padding: '10px',
  borderBottom: '1px solid var(--border-subtle)',
  fontSize: 12,
};

function Stat({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <div className="panel" style={{ padding: '14px 18px' }}>
      <div style={{ fontSize: 10, textTransform: 'uppercase', color: 'var(--text-faded)', letterSpacing: '0.08em' }}>
        {label}
      </div>
      <div style={{ fontSize: 22, fontWeight: 300, color: 'var(--text-strong)', marginTop: 6 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}
