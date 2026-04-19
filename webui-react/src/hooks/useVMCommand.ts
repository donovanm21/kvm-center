import { useMutation, useQueryClient } from '@tanstack/react-query';

import { apiPost } from '@/api/client';
import { useToast } from './useToast';

export type VMAction =
  | 'START' | 'STOP' | 'FORCESTOP' | 'RESTART'
  | 'SNAPSHOT' | 'REVERT_SNAPSHOT' | 'DELETE_SNAPSHOT'
  | 'MIGRATE' | 'OFFLINE_MIGRATE'
  | 'DELETE_VM'
  // v5.27: hardware edits (EDIT_VM takes vcpus + mem_mb),
  // live ISO swap on CDROM targets (CHANGE_MEDIA).
  | 'EDIT_VM'
  | 'CHANGE_MEDIA';

export interface CommandInput {
  host_id: string;
  vm_uuid: string;
  vm_name: string;
  action: VMAction;
  params?: Record<string, string>;
}

interface CommandResult { success?: boolean; message?: string; error?: string }

/**
 * Wraps POST /api/command. On success we preemptively invalidate the
 * relevant caches — the SSE "inventory" event will fire a moment later
 * and invalidate again, but the mutation-triggered invalidation gives
 * the immediate "I clicked Start, the state pill flipped instantly"
 * feedback the v3 UI never had.
 */
export function useVMCommand() {
  const qc = useQueryClient();
  const { toast } = useToast();

  return useMutation({
    mutationFn: (input: CommandInput) => apiPost<CommandResult>('/api/command', input),
    onSuccess: (data, input) => {
      if (data && data.success === false) {
        toast('error', `${input.action} failed: ${data.message || 'unknown error'}`);
        return;
      }
      toast('success', `${input.action} sent to ${input.vm_name}`);
      // The SSE event will also land in a few ms — belt and suspenders.
      qc.invalidateQueries({ queryKey: ['vms'] });
      qc.invalidateQueries({ queryKey: ['hosts'] });
    },
    onError: (err, input) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast('error', `${input.action} failed: ${msg}`);
    },
  });
}
