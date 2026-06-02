import type { QueryClient } from '@tanstack/react-query';
import { qk } from '@/queryKeys';

/**
 * SSE connection state surfaced through a tiny pub/sub so any
 * component can render a "live / reconnecting / disconnected" pill
 * without each subscribing to the EventSource directly. Pre-v5.50
 * the EventSource error path only logged to the console; operators
 * had no UI signal that "live" data was actually 60 s stale.
 */
export type SSEStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

let currentStatus: SSEStatus = 'closed';
const statusListeners = new Set<(s: SSEStatus) => void>();

function setSSEStatus(next: SSEStatus) {
  if (currentStatus === next) return;
  currentStatus = next;
  for (const fn of statusListeners) fn(next);
}

export function getSSEStatus(): SSEStatus { return currentStatus; }

export function subscribeSSEStatus(fn: (s: SSEStatus) => void): () => void {
  statusListeners.add(fn);
  fn(currentStatus); // immediate snapshot
  return () => { statusListeners.delete(fn); };
}

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
  setSSEStatus('connecting');
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

  es.onopen = () => {
    setSSEStatus('open');
  };

  es.onerror = () => {
    // EventSource auto-reconnects with backoff; surface the state so
    // the topbar pill can show "reconnecting…" while we wait. CLOSED
    // is the terminal state (will not reconnect — usually means the
    // /api/events handler returned 401).
    setSSEStatus(es.readyState === EventSource.CLOSED ? 'closed' : 'reconnecting');
  };

  return () => {
    es.close();
    setSSEStatus('closed');
  };
}
