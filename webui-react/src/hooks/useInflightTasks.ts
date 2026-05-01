import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';

interface TaskEntryLite {
  request_id: string;
  action: string;
  vm_uuid?: string;
  vm_name?: string;
  outcome: string;
  last_progress?: { progress_pct: number; phase?: string };
}

interface RecentTasksResponse {
  entries: TaskEntryLite[];
}

/**
 * Returns the in-flight task targeting a given VM, if any.
 * Backed by the same /api/tasks/recent query that powers the
 * Recent Tasks pane (PR-3b) so there's no extra network cost.
 *
 * Used by lifecycle action menus + the VM detail page (PR-3c)
 * to grey out conflicting buttons + show an "in flight" banner.
 */
export function useInflightTaskOnVM(vmUUID?: string, vmName?: string): TaskEntryLite | null {
  const { data } = useQuery<RecentTasksResponse>({
    queryKey: qk.tasks(),
    queryFn: () => apiGet<RecentTasksResponse>('/api/tasks/recent?limit=20'),
    staleTime: 1000,
  });
  if (!data?.entries) return null;
  for (const e of data.entries) {
    if (e.outcome !== 'pending') continue;
    if (vmUUID && e.vm_uuid && e.vm_uuid.toLowerCase() === vmUUID.toLowerCase()) return e;
    if (vmName && e.vm_name && e.vm_name === vmName) return e;
  }
  return null;
}
