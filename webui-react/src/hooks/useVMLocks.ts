import { useMemo } from 'react';

import type { LockReason } from '@/components/StatePill';
import type { VM } from '@/types';
import { useMigrations, useVMs } from './useInventory';

// Phases that mean the destination already had the VM defined when the
// migration failed. The agent's rollback path tries to undefine + delete
// transferred files, but partial state can survive a failed rollback —
// keep the destination row gated until an operator clears it.
export const PHASES_PAST_DEFINE = new Set(['define', 'start', 'verify', 'cleanup']);

/** Composite key for the lock map. Uses (host_id, vm_name) because the
 * agent's source-side telemetry can briefly report a "ghost" entry on
 * the source host with the same VM uuid as the destination — we still
 * want to lock the source row separately while the migration is in
 * flight. */
export function lockKey(hostID: string, vmName: string): string {
  return `${hostID}|${vmName}`;
}

/** Hover-tooltip text for a locked row. Shared between every VM list
 * view so the wording stays consistent. */
export function lockTitleFor(lock: LockReason | undefined): string {
  if (lock === 'migrating') return 'Migration in progress — actions are disabled until it finishes';
  if (lock === 'rolled-back') return 'Migration rolled back — destination may have partial artifacts. Investigate before acting.';
  return '';
}

/** useVMLocks returns a `lockOf(vm)` accessor that any view rendering
 * VM rows can use to gate edits / actions while a migration is running.
 *
 * Replaces three near-identical inline `locks` memos in VMs.tsx,
 * KVMTools.tsx, and HostDetail.tsx — keeping the predicate in one
 * place means any view that lists VMs picks up the lock automatically
 * (fix for #58: kvm-tools view bypassing the migration guard). */
export function useVMLocks(): {
  lockOf: (vm: Pick<VM, 'host_id' | 'name'>) => LockReason | undefined;
  isLoading: boolean;
} {
  const migrations = useMigrations();

  const map = useMemo(() => {
    const m = new Map<string, LockReason>();
    for (const r of migrations.data ?? []) {
      if (r.status === 'running' && r.source_host && r.vm_name) {
        m.set(lockKey(r.source_host, r.vm_name), 'migrating');
      } else if (r.status === 'failed' && r.dest_host && r.vm_name && PHASES_PAST_DEFINE.has(r.phase ?? '')) {
        m.set(lockKey(r.dest_host, r.vm_name), 'rolled-back');
      }
    }
    return m;
  }, [migrations.data]);

  return {
    lockOf: (vm) => map.get(lockKey(vm.host_id, vm.name)),
    isLoading: migrations.isLoading,
  };
}

// State buckets used by the dedup tiebreaker. Lower index = "more
// canonical" — prefer an actively-running VM over a stopped one when
// duplicates exist for the same uuid.
const STATE_PREFERENCE: Record<string, number> = {
  running: 0,
  paused: 1,
  pmsuspended: 2,
  blocked: 3,
  // libvirt's domain-state strings vary in spelling across versions —
  // include the common variants explicitly so neither flavour leaks
  // through as "unknown" (lowest priority).
  'shut off': 5,
  'shut-off': 5,
  shutoff: 5,
  shutdown: 6,
  crashed: 7,
};

function statePref(state: string | undefined): number {
  if (!state) return 9;
  const s = state.toLowerCase();
  return STATE_PREFERENCE[s] ?? 4; // unknown live-ish state ranks above shut-off
}

/** useDedupedVMs returns the same data shape as `useVMs` but collapses
 * duplicate uuid entries down to the most canonical row. During an
 * OFFLINE_MIGRATE the source agent can briefly report the VM in a
 * shut-off state on the source host while the destination agent has
 * already started reporting the running copy on the destination —
 * picking the more "live" entry stops the user seeing two or three
 * rows for the same VM (fix for #57). */
export function useDedupedVMs(): ReturnType<typeof useVMs> {
  const result = useVMs();
  const deduped = useMemo(() => {
    if (!result.data) return result.data;
    const byUuid = new Map<string, VM>();
    for (const vm of result.data) {
      const existing = byUuid.get(vm.uuid);
      if (!existing) {
        byUuid.set(vm.uuid, vm);
        continue;
      }
      // Tiebreak: prefer the entry whose state ranks higher (more live).
      // If both are equally live, prefer the later one in the list — the
      // backend orders by report time so this defaults to "freshest".
      if (statePref(vm.state) <= statePref(existing.state)) {
        byUuid.set(vm.uuid, vm);
      }
    }
    return Array.from(byUuid.values());
  }, [result.data]);

  return { ...result, data: deduped } as ReturnType<typeof useVMs>;
}
