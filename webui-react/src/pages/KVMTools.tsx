import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { apiPost } from '@/api/client';
import { PaginationBar, usePagination } from '@/components/Pagination';
import { SortableTH, useSort } from '@/components/SortableTH';
import { useAuth } from '@/hooks/useAuth';
import { useKVMToolsLatest, useVMs } from '@/hooks/useInventory';
import { useToast } from '@/hooks/useToast';
import type { VM } from '@/types';
import { qk } from '@/queryKeys';

type KVMToolsSortKey = 'name' | 'host_id' | 'state' | 'kvm_tools_version' | 'status';

type Filter = 'all' | 'warning' | 'current';

/**
 * KVM Tools update center: lists every VM with its reported kvm-tools
 * version, marks anything that isn't on the latest build, and lets the
 * operator queue a one-shot self-update for any subset. Updates are
 * delivered piggyback on the next telemetry POST — no SSH from the
 * server is needed.
 */
export function KVMTools() {
  const vms = useVMs();
  const latest = useKVMToolsLatest();
  const { hasPerm } = useAuth();
  const qc = useQueryClient();
  const { toast } = useToast();

  const [filter, setFilter] = useState<Filter>('all');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const canUpdate = hasPerm('vm.edit');

  // User-clickable sort; defaults to the operationally useful
  // "status ascending" which pushes warnings to the top since
  // STATUS_ORDER below buckets outdated/missing/no-tools before current.
  const { sort, onToggle, compare } = useSort<KVMToolsSortKey>('status');

  const latestVer = latest.data?.version ?? '';

  const rows = useMemo(() => {
    const list = vms.data ?? [];
    const enriched = list.map((v) => ({ vm: v, status: classify(v, latestVer) }));
    const filtered = enriched.filter((r) => {
      if (filter === 'all') return true;
      if (filter === 'warning') return r.status === 'outdated' || r.status === 'missing' || r.status === 'no-tools';
      if (filter === 'current') return r.status === 'current';
      return true;
    });
    return [...filtered].sort((a, b) => {
      // Map sort key → comparable scalar on each row. Status gets a
      // hand-ordered rank so "status asc" means "warnings first".
      const key: KVMToolsSortKey = sort.key;
      const valueOf = (r: typeof a) => {
        switch (key) {
          case 'status':            return STATUS_ORDER[r.status] ?? 9;
          case 'kvm_tools_version': return r.vm.kvm_tools_version ?? '';
          case 'state':             return r.vm.state ?? '';
          case 'host_id':           return r.vm.host_id ?? '';
          case 'name':              return r.vm.name ?? '';
        }
      };
      const cmp = compare(valueOf(a), valueOf(b));
      if (cmp !== 0) return cmp;
      return a.vm.name.localeCompare(b.vm.name);
    });
  }, [vms.data, latestVer, filter, sort, compare]);

  const counts = useMemo(() => {
    const list = vms.data ?? [];
    const c = { all: list.length, outdated: 0, missing: 0, current: 0, noTools: 0 };
    for (const v of list) {
      const s = classify(v, latestVer);
      if (s === 'outdated') c.outdated++;
      else if (s === 'missing') c.missing++;
      else if (s === 'current') c.current++;
      else if (s === 'no-tools') c.noTools++;
    }
    return c;
  }, [vms.data, latestVer]);

  const updateable = rows.filter((r) => r.status === 'outdated' || r.status === 'missing');
  const allSelected = updateable.length > 0 && updateable.every((r) => selected.has(r.vm.uuid));
  const someSelected = updateable.some((r) => selected.has(r.vm.uuid));

  const pg = usePagination(rows);

  const toggle = (uuid: string) => {
    setSelected((cur) => {
      const next = new Set(cur);
      if (next.has(uuid)) next.delete(uuid); else next.add(uuid);
      return next;
    });
  };
  const toggleAll = () => {
    setSelected((cur) => {
      if (allSelected) {
        const next = new Set(cur);
        for (const r of updateable) next.delete(r.vm.uuid);
        return next;
      }
      const next = new Set(cur);
      for (const r of updateable) next.add(r.vm.uuid);
      return next;
    });
  };

  const queueUpdate = useMutation({
    mutationFn: (uuids: string[]) => apiPost<{ status: string; count: number }>('/api/kvm-tools/update', { uuids }),
    onSuccess: (d, uuids) => {
      toast('success', `Queued ${d.count} update${d.count === 1 ? '' : 's'} — will run on next telemetry tick (~30 s)`);
      // Drop them from the local selection now that they're queued.
      setSelected((cur) => {
        const next = new Set(cur);
        for (const u of uuids) next.delete(u);
        return next;
      });
      qc.invalidateQueries({ queryKey: qk.vms() });
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast('error', `Queue failed: ${msg}`);
    },
  });

  const onUpdateSelected = () => {
    const uuids = Array.from(selected);
    if (uuids.length === 0) return;
    queueUpdate.mutate(uuids);
  };

  return (
    <>
      <div className="panel" style={{ borderLeft: '3px solid var(--accent)', marginBottom: 14 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
          <div>
            <div style={{ fontSize: 10, textTransform: 'uppercase', color: 'var(--text-faded)', letterSpacing: '0.08em' }}>Latest kvm-tools</div>
            <div style={{ fontSize: 18, fontWeight: 600, color: 'var(--text-strong)' }}>
              {latest.isLoading ? '…' : latestVer || 'unknown'}
            </div>
          </div>
          <div style={{ flex: 1 }} />
          <PillCount label="Outdated" value={counts.outdated} tone="warn" />
          <PillCount label="Missing"  value={counts.missing}  tone="danger" />
          <PillCount label="Current"  value={counts.current}  tone="ok" />
          <PillCount label="No tools" value={counts.noTools}  tone="muted" />
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, flexWrap: 'wrap' }}>
        <FilterTab label={`All (${counts.all})`} active={filter === 'all'} onClick={() => setFilter('all')} />
        <FilterTab
          label={`Warnings (${counts.outdated + counts.missing + counts.noTools})`}
          active={filter === 'warning'}
          onClick={() => setFilter('warning')}
        />
        <FilterTab label={`Current (${counts.current})`} active={filter === 'current'} onClick={() => setFilter('current')} />
        <div style={{ flex: 1 }} />
        <button
          type="button"
          className="btn-primary-sm"
          onClick={onUpdateSelected}
          disabled={!canUpdate || selected.size === 0 || queueUpdate.isPending}
          title={!canUpdate ? 'Requires vm.edit' : ''}
        >
          {queueUpdate.isPending ? 'Queueing…' : `Update ${selected.size} selected`}
        </button>
      </div>

      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {rows.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: 'var(--text-muted)' }}>
            {filter === 'warning' && counts.outdated + counts.missing + counts.noTools === 0
              ? 'All guests on the latest kvm-tools — nothing to update.'
              : 'No VMs match this filter.'}
          </div>
        ) : (
          <>
          <div className="data-table-scroll">
          <table className="data-table">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    type="checkbox"
                    aria-label="Select all updateable"
                    checked={allSelected}
                    ref={(el) => { if (el) el.indeterminate = !allSelected && someSelected; }}
                    onChange={toggleAll}
                  />
                </th>
                <SortableTH colKey="name"              label="VM"               sort={sort} onToggle={onToggle} />
                <SortableTH colKey="host_id"           label="Host"             sort={sort} onToggle={onToggle} />
                <SortableTH colKey="state"             label="State"            sort={sort} onToggle={onToggle} />
                <SortableTH colKey="kvm_tools_version" label="Reported version" sort={sort} onToggle={onToggle} />
                <SortableTH colKey="status"            label="Status"           sort={sort} onToggle={onToggle} />
              </tr>
            </thead>
            <tbody>
              {pg.items.map(({ vm, status }) => {
                const checked = selected.has(vm.uuid);
                const updateable = status === 'outdated' || status === 'missing';
                return (
                  <tr key={vm.uuid}>
                    <td>
                      <input
                        type="checkbox"
                        checked={checked}
                        disabled={!updateable}
                        onChange={() => toggle(vm.uuid)}
                        aria-label={`Select ${vm.name}`}
                      />
                    </td>
                    <td>
                      <Link to={`/vms/${encodeURIComponent(vm.uuid)}`} style={{ color: 'var(--text-strong)', fontWeight: 500 }}>
                        {vm.name}
                      </Link>
                    </td>
                    <td style={{ color: 'var(--text-muted)' }}>{vm.host_id}</td>
                    <td><span className={`state-pill ${vm.state}`}>{vm.state}</span></td>
                    <td style={{ fontFamily: "'SF Mono', monospace", fontSize: 12, color: 'var(--text-body)' }}>
                      {vm.kvm_tools_version || (vm.guest_agent ? '—' : <span style={{ color: 'var(--text-faded)' }}>not reporting</span>)}
                    </td>
                    <td>
                      <StatusPill status={status} queued={vm.kvm_tools_update_queued} />
                      <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 2 }}>
                        {statusDetail(status, vm, latestVer)}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          </div>
          <PaginationBar pagination={pg} noun="VMs" />
          </>
        )}
      </div>

      <p style={{ marginTop: 12, fontSize: 11, color: 'var(--text-faded)' }}>
        Updates are delivered on the next telemetry tick (~30 s). The in-guest daemon
        downloads the new binary, replaces itself, and systemd brings it back up. No
        SSH from the server required.
      </p>
    </>
  );
}

type Status = 'current' | 'outdated' | 'missing' | 'no-tools';

// Hand-ordered rank so sorting the status column surfaces the most
// operationally relevant rows first. outdated → missing → no-tools →
// current (current last because it's the "nothing to do" bucket).
const STATUS_ORDER: Record<Status, number> = {
  outdated: 0,
  missing: 1,
  'no-tools': 2,
  current: 3,
};

function classify(v: VM, latest: string): Status {
  if (!v.guest_agent) return 'no-tools';
  if (!v.kvm_tools_version) return 'missing';
  if (!latest || v.kvm_tools_version === latest) return 'current';
  return 'outdated';
}

// statusDetail expands the status pill with a one-liner describing the
// specific issue — the user should be able to read the table and know
// exactly why each warning row is flagged.
function statusDetail(status: Status, v: VM, latest: string): string {
  switch (status) {
    case 'current':
      return `Running ${latest}`;
    case 'outdated':
      return `Reporting ${v.kvm_tools_version} — server ships ${latest}`;
    case 'missing':
      return v.state === 'running'
        ? 'Guest agent live but kvm-tools version not reported — older daemon or qemu-ga fallback'
        : 'VM is not running — start it, then install kvm-tools';
    case 'no-tools':
      return v.state === 'running'
        ? 'No guest agent responding — install kvm-tools to enable OS / IP / filesystem reporting'
        : 'VM is not running — no telemetry available';
    default:
      return '';
  }
}

function StatusPill({ status, queued }: { status: Status; queued?: boolean }) {
  if (queued) return <span className="tone-pill warn">QUEUED</span>;
  const tone = STATUS_TONE[status];
  return <span className={`tone-pill ${tone.tone}`}>{tone.label}</span>;
}

const STATUS_TONE: Record<Status, { tone: 'ok' | 'warn' | 'danger' | 'muted'; label: string }> = {
  current:    { tone: 'ok',     label: 'CURRENT' },
  outdated:   { tone: 'warn',   label: 'OUTDATED' },
  missing:    { tone: 'danger', label: 'MISSING' },
  'no-tools': { tone: 'muted',  label: 'NO TOOLS' },
};

function PillCount({ label, value, tone }: { label: string; value: number; tone: 'ok' | 'warn' | 'danger' | 'muted' }) {
  return (
    <div className={`tone-panel ${tone}`} style={{ padding: '6px 12px', minWidth: 80, textAlign: 'center' }}>
      <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', opacity: 0.8 }}>{label}</div>
      <div style={{ fontSize: 18, fontWeight: 600 }}>{value}</div>
    </div>
  );
}

function FilterTab({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        padding: '6px 12px', height: 30,
        background: active ? 'var(--accent)' : 'var(--surface-2)',
        color: active ? '#fff' : 'var(--text-body)',
        border: '1px solid ' + (active ? 'var(--accent)' : 'var(--border)'),
        borderRadius: 4, fontSize: 12, fontWeight: 500, fontFamily: 'inherit',
        cursor: 'pointer',
      }}
    >
      {label}
    </button>
  );
}
