import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';
import { Modal } from './Modal';

// vSphere-parity Recent Tasks pane (v5.48.1 PR-3b).
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

export function RecentTasks() {
  const [collapsed, setCollapsed] = useState(false);
  const [openTask, setOpenTask] = useState<TaskEntry | null>(null);
  const [resumeBanner, setResumeBanner] = useState<TaskEntry[] | null>(null);
  // Track the set of request_ids we've already shown a banner for
  // — so reopening the tab a second time doesn't re-toast the same
  // completions. Persists across renders but not across reloads
  // (page refresh re-reads /api/tasks/recent which is the
  // intentional "what happened while I was away" view).
  const seenTerminalIDs = useRef<Set<string>>(new Set());

  const { data, isError } = useQuery<RecentTasksResponse>({
    queryKey: qk.tasks(),
    queryFn: () => apiGet<RecentTasksResponse>('/api/tasks/recent?limit=20'),
    refetchInterval: POLL_INTERVAL_MS,
    // The SSE invalidator triggers a refetch immediately when a
    // task transitions; the poll is a safety net.
  });

  // PR-3d auto-resume: when the tab regains focus, surface any
  // tasks that completed since the last time the user looked.
  useEffect(() => {
    const onFocus = () => {
      const newlyTerminal = (data?.entries ?? [])
        .filter((e) => e.outcome !== 'pending')
        .filter((e) => !seenTerminalIDs.current.has(e.request_id));
      // First focus after mount — populate the seen set without
      // banner-spamming the user with everything in their recent
      // history. Subsequent focuses surface only NEW completions.
      if (seenTerminalIDs.current.size === 0) {
        for (const e of data?.entries ?? []) seenTerminalIDs.current.add(e.request_id);
        return;
      }
      if (newlyTerminal.length > 0) {
        for (const e of newlyTerminal) seenTerminalIDs.current.add(e.request_id);
        setResumeBanner(newlyTerminal);
      }
    };
    window.addEventListener('focus', onFocus);
    // Run once on mount to seed seenTerminalIDs.
    onFocus();
    return () => window.removeEventListener('focus', onFocus);
  }, [data]);

  if (isError) return null; // gracefully hide on auth issues

  const entries = data?.entries ?? [];
  const inFlight = entries.filter((e) => e.outcome === 'pending');

  if (entries.length === 0 && !resumeBanner) return null;

  return (
    <>
      {resumeBanner && resumeBanner.length > 0 && (
        <div className="task-resume-banner" role="status">
          <span>
            {resumeBanner.length === 1
              ? `Task ${resumeBanner[0].action} on ${resumeBanner[0].vm_name ?? resumeBanner[0].host_id} ${resumeBanner[0].outcome} while you were away.`
              : `${resumeBanner.length} tasks completed while you were away.`}
          </span>
          <button onClick={() => setResumeBanner(null)} aria-label="Dismiss">
            ×
          </button>
        </div>
      )}
      <aside className={'recent-tasks' + (collapsed ? ' collapsed' : '')}>
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
              </li>
            ))}
          </ul>
        )}
      </aside>
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
