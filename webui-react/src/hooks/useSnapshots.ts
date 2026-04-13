import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useToast } from './useToast';
import type { Snapshot } from '@/types';

/**
 * Snapshot list for a given (host, VM). Polled manually (not on SSE)
 * because the list rarely changes outside of explicit user actions.
 * The create/revert/delete mutations invalidate this key directly.
 *
 * The agent returns `{ snapshots: Snapshot[] }` (v3 used the same
 * shape). Unwrap it here so callers always receive a bare array.
 */
export function useSnapshots(hostID: string, vmName: string) {
  return useQuery({
    queryKey: ['snapshots', hostID, vmName],
    queryFn: async () => {
      const resp = await apiGet<{ snapshots?: Snapshot[] } | Snapshot[]>(
        `/api/vms/snapshots?host=${encodeURIComponent(hostID)}&vm=${encodeURIComponent(vmName)}`,
      );
      if (Array.isArray(resp)) return resp;
      return resp?.snapshots ?? [];
    },
    enabled: Boolean(hostID && vmName),
  });
}

interface SnapshotParams { host_id: string; vm_uuid: string; vm_name: string; name: string }

export function useCreateSnapshot() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (p: SnapshotParams) =>
      apiPost('/api/command', {
        host_id: p.host_id,
        vm_uuid: p.vm_uuid,
        vm_name: p.vm_name,
        action: 'SNAPSHOT',
        params: { name: p.name },
      }),
    onSuccess: (_d, p) => {
      toast('success', `Snapshot "${p.name}" created`);
      qc.invalidateQueries({ queryKey: ['snapshots', p.host_id, p.vm_name] });
      qc.invalidateQueries({ queryKey: ['vms'] });
    },
    onError: (err) => {
      toast('error', 'Snapshot failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}

export function useRevertSnapshot() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (p: SnapshotParams) =>
      apiPost('/api/command', {
        host_id: p.host_id,
        vm_uuid: p.vm_uuid,
        vm_name: p.vm_name,
        action: 'REVERT_SNAPSHOT',
        params: { name: p.name },
      }),
    onSuccess: (_d, p) => {
      toast('success', `Reverted to "${p.name}"`);
      qc.invalidateQueries({ queryKey: ['snapshots', p.host_id, p.vm_name] });
      qc.invalidateQueries({ queryKey: ['vms'] });
    },
    onError: (err) => {
      toast('error', 'Revert failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}

export function useDeleteSnapshot() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (p: SnapshotParams) =>
      apiPost('/api/command', {
        host_id: p.host_id,
        vm_uuid: p.vm_uuid,
        vm_name: p.vm_name,
        action: 'DELETE_SNAPSHOT',
        params: { name: p.name },
      }),
    onSuccess: (_d, p) => {
      toast('success', `Snapshot "${p.name}" deleted`);
      qc.invalidateQueries({ queryKey: ['snapshots', p.host_id, p.vm_name] });
    },
    onError: (err) => {
      toast('error', 'Delete failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}
