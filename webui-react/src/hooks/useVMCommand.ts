import { useMutation, useQueryClient } from '@tanstack/react-query';

import { apiPost } from '@/api/client';
import { useToast } from './useToast';
import { qk } from '@/queryKeys';

export type VMAction =
  | 'START' | 'STOP' | 'FORCESTOP' | 'RESTART' | 'POWER_CYCLE'
  | 'SNAPSHOT' | 'REVERT_SNAPSHOT' | 'DELETE_SNAPSHOT'
  | 'MIGRATE' | 'OFFLINE_MIGRATE'
  | 'DELETE_VM'
  // v5.27: hardware edits (EDIT_VM takes vcpus + mem_mb),
  // live ISO swap on CDROM targets (CHANGE_MEDIA).
  // v5.30: POWER_CYCLE = proper shutdown+start (config changes
  // like memory cap only apply on a fresh qemu process); RESTART
  // remains an in-guest ACPI reboot.
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
      // Prefer the agent's own message when it carries useful detail
      // (e.g. EDIT_VM saying "vcpus=4 (config-only, reboot to apply)").
      // Fall back to the terse "action sent" summary for commands
      // whose agent response is a generic "ok".
      const detail = data?.message && !/^(ok|done|success)$/i.test(data.message.trim())
        ? `${input.action}: ${data.message}`
        : `${input.action} sent to ${input.vm_name}`;
      toast('success', detail);
      // The SSE event will also land in a few ms — belt and suspenders.
      qc.invalidateQueries({ queryKey: qk.vms() });
      qc.invalidateQueries({ queryKey: qk.hosts() });
      // Migration actions create a record server-side the moment the
      // command fires. Invalidate so the /migrations page + the
      // MigrationPill on VMDetail pick it up without waiting on the
      // 60 s refetch or an SSE round-trip.
      if (input.action === 'MIGRATE' || input.action === 'OFFLINE_MIGRATE') {
        qc.invalidateQueries({ queryKey: qk.migrations() });
      }
    },
    onError: (err, input) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast('error', `${input.action} failed: ${msg}`);
    },
  });
}
