import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState, useRef } from 'react';

import { apiGet, apiPost } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { qk } from '@/queryKeys';
import { Modal } from './Modal';

// Recent Tasks pane (v5.48.1 PR-3b).
//
// Pinned to the bottom of every screen via the Layout. Subscribes
// to SSE `command` + `command_progress` events (wired through the
// global query-invalidator in api/events.ts) so it redraws in real
// time. Click a row → modal with full detail.
//
// The pane self-collapses when there's nothing in flight to keep
// the dashboard breathing room. A small badge on the corner-pip
// shows the in-flight count even when collapsed.

interface CommandProgress {
  request_id: string;
  phase?: string;
  progress_pct: number;
  detail?: string;
  reported_at?: number;
}

interface TaskEntry {
  request_id: string;
  action: string;
  host_id: string;
  vm_name?: string;
  vm_uuid?: string;
  actor_id?: string;
  submitted_at: string;
  applied_at?: string;
  outcome: string;
  status_code?: number;
  message?: string;
  inflight_until?: string;
  last_progress?: CommandProgress;
}

interface RecentTasksResponse {
  entries: TaskEntry[];
  count: number;
}

const POLL_INTERVAL_MS = 5000;

function fmtDuration(start: string, end?: string): string {
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  const ms = Math.max(0, e - s);
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const sec = Math.floor((ms % 60_000) / 1000);
  return `${m}m${sec}s`;
}

function outcomeClass(outcome: string): string {
  switch (outcome) {
    case 'pending':
      return 'task-pending';
    case 'success':
      return 'task-success';
    case 'failed':
      return 'task-failed';
    case 'cancelled':
      return 'task-cancelled';
    case 'unknown':
      return 'task-unknown';
    default:
      return '';
  }
}

// CANCELLABLE_ACTIONS mirrors the agent's async-eligible set
// (PR-2e) — only async / long actions can be meaningfully
// cancelled. START / RESTART / SNAPSHOT etc. are sub-second.
const CANCELLABLE_ACTIONS = new Set([
  'DELETE_SNAPSHOT',
  'OFFLINE_MIGRATE',
  'MIGRATE',
  'DELETE_VM',
  'POWER_CYCLE',
]);

export function RecentTasks() {
  const qc = useQueryClient();
  const { hasPerm } = useAuth();
  // v5.48.6: default collapsed — pane only opens on explicit
  // header click. Click-outside collapses it again so it's never
  // unexpectedly covering content.
  const [collapsed, setCollapsed] = useState(true);
  const [openTask, setOpenTask] = useState<TaskEntry | null>(null);
  const [confirmCancel, setConfirmCancel] = useState<TaskEntry | null>(null);
  const paneRef = useRef<HTMLElement | null>(null);

  const cancelMut = useMutation({
    mutationFn: (req: { id: string; reason: string }) =>
      apiPost(`/api/command/cancel?request_id=${encodeURIComponent(req.id)}`, { reason: req.reason }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.tasks() }),
  });

  const { data, isError } = useQuery<RecentTasksResponse>({
    queryKey: qk.tasks(),
    queryFn: () => apiGet<RecentTasksResponse>('/api/tasks/recent?limit=20'),
    refetchInterval: POLL_INTERVAL_MS,
    // The SSE invalidator triggers a refetch immediately when a
    // task transitions; the poll is a safety net.
  });

  // v5.58.3: the "Task ACTION on HOST success while you were away"
  // banner used to fire on tab-focus. Operator feedback was that it
  // duplicated information the Recent Tasks pane already shows (and
  // looked stale by the time you noticed it). Removed — the pill on
  // the pane header is now the single surface for "what just
  // happened": `N running` badge for in-flight, click-to-expand for
  // recent history.

  // v5.48.6: click-outside the expanded pane collapses it. Skipped
  // while collapsed (no listener overhead). Modal portals are
  // excluded so detail / cancel-confirm modals don't dismiss the
  // pane behind them.
  useEffect(() => {
    if (collapsed) return;
    const onDocClick = (e: MouseEvent) => {
      const target = e.target as Node | null;
      if (!target) return;
      if (paneRef.current && paneRef.current.contains(target)) return;
      const modal = (target as Element).closest?.('.modal, .modal-content, .kvm-modal');
      if (modal) return;
      setCollapsed(true);
    };
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  }, [collapsed]);

  if (isError) return null; // gracefully hide on auth issues

  const entries = data?.entries ?? [];
  const inFlight = entries.filter((e) => e.outcome === 'pending');

  if (entries.length === 0) return null;

  return (
    <>
      <aside
        ref={paneRef}
        className={'recent-tasks' + (collapsed ? ' collapsed' : '')}
      >
        <header
          className="recent-tasks-header"
          onClick={() => setCollapsed((v) => !v)}
          role="button"
          tabIndex={0}
        >
          <span>Recent Tasks</span>
          {inFlight.length > 0 && (
            <span className="recent-tasks-badge">{inFlight.length} running</span>
          )}
          <span className="recent-tasks-toggle">{collapsed ? '▲' : '▼'}</span>
        </header>
        {!collapsed && (
          <ul className="recent-tasks-list">
            {entries.slice(0, 8).map((e) => (
              <li
                key={e.request_id}
                className={'recent-task-row ' + outcomeClass(e.outcome)}
                onClick={() => setOpenTask(e)}
              >
                <span className="task-action">{e.action}</span>
                <span className="task-target">
                  {e.vm_name || e.host_id}
                </span>
                {e.last_progress && e.outcome === 'pending' && (
                  <span
                    className="task-progress"
                    aria-label={`${e.last_progress.progress_pct}% — ${e.last_progress.phase ?? ''}`}
                  >
                    <span
                      className="task-progress-bar"
                      style={{ width: `${e.last_progress.progress_pct}%` }}
                    />
                    <span className="task-progress-label">
                      {e.last_progress.progress_pct}% {e.last_progress.phase ?? ''}
                    </span>
                  </span>
                )}
                <span className="task-outcome">{e.outcome}</span>
                <span className="task-duration">
                  {fmtDuration(e.submitted_at, e.applied_at)}
                </span>
                {e.outcome === 'pending' &&
                CANCELLABLE_ACTIONS.has(e.action) &&
                hasPerm('vm.lifecycle') ? (
                  <button
                    className="task-cancel-btn"
                    aria-label={`Cancel ${e.action}`}
                    onClick={(ev) => {
                      ev.stopPropagation();
                      setConfirmCancel(e);
                    }}
                  >
                    ×
                  </button>
                ) : (
                  <span className="task-cancel-spacer" />
                )}
              </li>
            ))}
          </ul>
        )}
      </aside>
      {confirmCancel && (
        <Modal
          open
          title="Cancel running task?"
          onClose={() => setConfirmCancel(null)}
          width={400}
          footer={
            <>
              <button onClick={() => setConfirmCancel(null)}>Keep running</button>
              <button
                className="btn-danger"
                disabled={cancelMut.isPending}
                onClick={() => {
                  if (!confirmCancel) return;
                  cancelMut.mutate(
                    { id: confirmCancel.request_id, reason: 'cancelled from Recent Tasks pane' },
                    {
                      onSettled: () => setConfirmCancel(null),
                    },
                  );
                }}
              >
                {cancelMut.isPending ? 'Cancelling…' : `Cancel ${confirmCancel.action}`}
              </button>
            </>
          }
        >
          <p>
            Cancel <strong>{confirmCancel.action}</strong> on{' '}
            <strong>{confirmCancel.vm_name ?? confirmCancel.host_id}</strong>?
          </p>
          <p style={{ opacity: 0.75, fontSize: 12 }}>
            The agent will SIGTERM the running process (with 5 s grace, then SIGKILL).
            For migrations, <code>virsh domjobabort</code> is also invoked so libvirtd
            can clean up its state machine. The task's outcome will be marked{' '}
            <code>cancelled</code>.
          </p>
        </Modal>
      )}
      {openTask && (
        <Modal
          open
          title={`Task ${openTask.request_id}`}
          onClose={() => setOpenTask(null)}
        >
          <dl className="task-detail">
            <dt>Action</dt>
            <dd>{openTask.action}</dd>
            <dt>Target</dt>
            <dd>
              {openTask.vm_name ? `${openTask.vm_name} on ${openTask.host_id}` : openTask.host_id}
            </dd>
            <dt>Outcome</dt>
            <dd className={outcomeClass(openTask.outcome)}>{openTask.outcome}</dd>
            <dt>Submitted</dt>
            <dd>{new Date(openTask.submitted_at).toLocaleString()}</dd>
            {openTask.applied_at && (
              <>
                <dt>Completed</dt>
                <dd>
                  {new Date(openTask.applied_at).toLocaleString()} ·{' '}
                  {fmtDuration(openTask.submitted_at, openTask.applied_at)}
                </dd>
              </>
            )}
            {openTask.actor_id && (
              <>
                <dt>Initiated by</dt>
                <dd>{openTask.actor_id}</dd>
              </>
            )}
            {openTask.message && (
              <>
                <dt>Message</dt>
                <dd>{openTask.message}</dd>
              </>
            )}
            {openTask.last_progress && (
              <>
                <dt>Last progress</dt>
                <dd>
                  {openTask.last_progress.progress_pct}% · {openTask.last_progress.phase ?? '—'}
                  {openTask.last_progress.detail && (
                    <div className="task-progress-detail">{openTask.last_progress.detail}</div>
                  )}
                </dd>
              </>
            )}
          </dl>
        </Modal>
      )}
    </>
  );
}
