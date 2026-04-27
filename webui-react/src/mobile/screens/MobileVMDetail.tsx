import { useQuery } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import type { VM } from '@/types';

import { VMStatePill } from './MobileHostDetail';

export function MobileVMDetail() {
  const { uuid = '' } = useParams<{ uuid: string }>();
  const nav = useNavigate();

  const vms = useQuery({ queryKey: qk.vms(), queryFn: () => apiGet<VM[]>('/api/vms') });
  const vm = (vms.data ?? []).find((v) => v.uuid === uuid);

  return (
    <div className="m-screen" data-testid="mobile-vm-detail">
      <button type="button" className="m-back" onClick={() => nav('/m/vms')}>← VMs</button>

      {vms.isLoading && <div className="m-placeholder">Loading…</div>}
      {!vms.isLoading && !vm && <div className="m-error">VM not found.</div>}

      {vm && (
        <>
          <div className="m-screen-h">
            <h1 className="m-h1">{vm.name}</h1>
            <p className="m-sub">
              <VMStatePill state={vm.state} />
              <span style={{ marginLeft: 8 }}>{vm.host_id}</span>
            </p>
          </div>

          <div className="m-card">
            <div className="m-section-h" style={{ marginBottom: 10 }}><h2>Resources</h2></div>
            <dl className="m-kv">
              <dt>vCPU</dt><dd>{vm.cpu_count} ({Math.round(vm.cpu_usage)}% used)</dd>
              <dt>Memory</dt><dd>{(vm.mem_total_mb / 1024).toFixed(1)} GiB ({Math.round((vm.mem_usage_mb / Math.max(1, vm.mem_total_mb)) * 100)}% used)</dd>
              <dt>Disk</dt><dd>{vm.disk_usage_gb.toFixed(1)} / {vm.disk_total_gb.toFixed(1)} GiB</dd>
              <dt>Snapshots</dt><dd>{vm.snapshot_count}</dd>
              {vm.firmware && <><dt>Firmware</dt><dd>{vm.firmware}</dd></>}
              {vm.machine_type && <><dt>Machine</dt><dd>{vm.machine_type}</dd></>}
            </dl>
          </div>

          {vm.guest_agent && (
            <div className="m-card">
              <div className="m-section-h" style={{ marginBottom: 10 }}><h2>Guest</h2></div>
              <dl className="m-kv">
                {vm.guest_os && <><dt>OS</dt><dd>{vm.guest_os}</dd></>}
                {vm.guest_hostname && <><dt>Hostname</dt><dd>{vm.guest_hostname}</dd></>}
                {vm.guest_kernel && <><dt>Kernel</dt><dd>{vm.guest_kernel}</dd></>}
                {vm.guest_uptime && <><dt>Uptime</dt><dd>{vm.guest_uptime}</dd></>}
                {vm.kvm_tools_version && <><dt>kvm-tools</dt><dd>{vm.kvm_tools_version}</dd></>}
                {vm.guest_ips && vm.guest_ips.length > 0 && (
                  <><dt>IPs</dt><dd>{vm.guest_ips.join(', ')}</dd></>
                )}
              </dl>
            </div>
          )}

          {vm.guest_fs && vm.guest_fs.length > 0 && (
            <div className="m-card">
              <div className="m-section-h" style={{ marginBottom: 10 }}><h2>Filesystems</h2></div>
              {vm.guest_fs.map((fs) => {
                const pct = fs.total_gb > 0 ? Math.round((fs.used_gb / fs.total_gb) * 100) : 0;
                const tone = pct >= 90 ? 'danger' : pct >= 75 ? 'warn' : '';
                return (
                  <div key={fs.mountpoint} style={{ marginBottom: 10 }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }}>
                      <span style={{ color: 'var(--text-strong)' }}>{fs.mountpoint}</span>
                      <span style={{ color: 'var(--text-muted)' }}>{fs.used_gb.toFixed(1)} / {fs.total_gb.toFixed(1)} GiB</span>
                    </div>
                    <div className="m-meter"><div className={`m-meter-fill ${tone}`} style={{ width: `${pct}%` }} /></div>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </div>
  );
}
