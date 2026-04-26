import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import type { Host, VM } from '@/types';

export function MobileHostDetail() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();

  const hosts = useQuery({ queryKey: qk.hosts(), queryFn: () => apiGet<Host[]>('/api/hosts') });
  const vms = useQuery({ queryKey: qk.vms(), queryFn: () => apiGet<VM[]>('/api/vms') });

  const host = (hosts.data ?? []).find((h) => h.id === id);
  const hostVMs = (vms.data ?? []).filter((v) => v.host_id === id);

  return (
    <div data-testid="mobile-host-detail">
      <button type="button" className="m-back" onClick={() => nav('/m/hosts')}>← Hosts</button>

      {hosts.isLoading && <div className="m-placeholder">Loading…</div>}
      {!hosts.isLoading && !host && <div className="m-error">Host “{id}” not found.</div>}

      {host && (
        <>
          <h1 className="m-h1">{host.id}</h1>
          <p className="m-sub">
            <span className={`m-pill ${host.status === 'online' ? 'ok' : 'danger'}`}>{host.status}</span>
            <span style={{ marginLeft: 8 }}>{host.ip}</span>
          </p>

          <div className="m-card">
            <dl className="m-kv">
              <dt>OS</dt><dd>{host.os || '—'}</dd>
              <dt>Kernel</dt><dd>{host.kernel_ver || '—'}</dd>
              <dt>libvirt</dt><dd>{host.libvirt_ver || '—'}</dd>
              <dt>CPU</dt><dd>{host.cpu_model || '—'}</dd>
              <dt>Cores</dt><dd>{host.cpu_count} threads</dd>
              <dt>Memory</dt><dd>{(host.mem_total_mb / 1024).toFixed(1)} GiB</dd>
              <dt>Uptime</dt><dd>{host.uptime || '—'}</dd>
              <dt>VMs</dt><dd>{host.vm_count}</dd>
            </dl>
          </div>

          <div className="m-section-h"><h2>VMs on this host</h2></div>
          {hostVMs.length === 0 && <div className="m-placeholder">No VMs.</div>}
          {hostVMs.map((v) => (
            <Link key={v.uuid} to={`/m/vms/${encodeURIComponent(v.uuid)}`} className="m-card-link">
              <div className="m-card">
                <div className="m-card-row">
                  <div style={{ minWidth: 0 }}>
                    <div className="m-card-title">{v.name}</div>
                    <div className="m-card-meta">{v.cpu_count} vCPU · {(v.mem_total_mb / 1024).toFixed(1)} GiB</div>
                  </div>
                  <VMStatePill state={v.state} />
                </div>
              </div>
            </Link>
          ))}
        </>
      )}
    </div>
  );
}

export function VMStatePill({ state }: { state: string }) {
  const tone = state === 'running' ? 'ok' : state === 'crashed' || state === 'unknown' ? 'danger' : state === 'paused' ? 'warn' : 'muted';
  return <span className={`m-pill ${tone}`}>{state}</span>;
}
