import type { QueryClient } from '@tanstack/react-query';

// Map a server-side event type to the React Query cache keys that
// should be invalidated. Keeping the table here — rather than inline in
// a hook — means adding a new event type is a one-line change and keeps
// the server ignorant of the client's cache layout.
const INVALIDATION_MAP: Record<string, readonly (readonly string[])[]> = {
  inventory: [['hosts'], ['vms'], ['storage']],
  migration: [['migrations']],
  settings: [['features']],
  users: [['users']],
  groups: [['groups']],
  roles: [['roles']],
  idps: [['idps']],
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
