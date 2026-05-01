import type { QueryClient } from '@tanstack/react-query';
import { qk } from '@/queryKeys';

// Map a server-side event type to the React Query cache keys that
// should be invalidated. Keeping the table here — rather than inline in
// a hook — means adding a new event type is a one-line change and keeps
// the server ignorant of the client's cache layout.
const INVALIDATION_MAP: Record<string, readonly (readonly string[])[]> = {
  inventory: [qk.hosts(), qk.vms(), qk.storage()],
  migration: [qk.migrations()],
  settings: [qk.features()],
  users: [qk.users()],
  groups: [qk.groups()],
  roles: [qk.roles()],
  idps: [qk.idps()],
  // v5.48.1 (PR-3a/3b): a command transitioned or streamed a
  // progress frame. Both invalidate the tasks list so the Recent
  // Tasks pane redraws. Keep the list small — high-frequency
  // command_progress events can fire several times per second on
  // a busy fleet.
  command: [qk.tasks()],
  command_progress: [qk.tasks()],
};

/**
 * Opens the SSE stream and hooks each event type to its query-key set.
 * Returns a disposer the caller invokes on unmount / logout.
 *
 * The browser's EventSource handles reconnection with a growing backoff
 * automatically, so we don't need our own retry loop — but we do want
 * to clean up on logout to avoid a ghost connection authed as the
 * previous user.
 */
export function subscribeToServerEvents(qc: QueryClient): () => void {
  const es = new EventSource('/api/events');

  const invalidate = (type: string) => {
    const targets = INVALIDATION_MAP[type];
    if (!targets) return;
    for (const key of targets) {
      qc.invalidateQueries({ queryKey: [...key] });
    }
  };

  for (const type of Object.keys(INVALIDATION_MAP)) {
    es.addEventListener(type, () => invalidate(type));
  }

  es.onerror = () => {
    // Transient; the browser reconnects with backoff. Log once per
    // transition rather than every tick to avoid console spam.
    if (es.readyState === EventSource.CLOSED) {
      console.warn('SSE connection closed — browser will reconnect');
    }
  };

  return () => es.close();
}
