// StatePill + LockBadge — shared rendering for VM / host status and
// migration locks. Both used to be inline span+className expressions
// scattered across VMs.tsx, VMDetail.tsx, HostDetail.tsx, Hosts.tsx,
// Dashboard.tsx, and Snapshots — they drifted in styling and lacked
// accessibility hooks. Tier 2 P2 #11 + #14 (v5.37.0).
//
// Accessibility: each pill carries a leading glyph (CSS dot is
// decorative only) plus an aria-label so colourblind operators and
// screen readers see the meaning, not just the colour.

import type { VMState } from '@/types';

export type LockReason = 'migrating' | 'rolled-back';

const STATE_GLYPHS: Record<string, string> = {
  running: '●',
  stopped: '■',
  paused: '⏸',
  crashed: '✕',
  unknown: '?',
};

const STATE_DESCRIPTIONS: Record<string, string> = {
  running: 'Running',
  stopped: 'Stopped',
  paused: 'Paused',
  crashed: 'Crashed',
  unknown: 'Unknown state',
};

/**
 * StatePill renders a coloured pill for a VM / host state string.
 * Falls back to the raw string for any state we don't have a dedicated
 * style for so future libvirt additions still display.
 */
export function StatePill({
  state,
  label,
}: {
  state: VMState | string;
  /** Optional override label — defaults to the state string. */
  label?: string;
}) {
  const stateClass = STATE_DESCRIPTIONS[state] ? state : 'unknown';
  const description = STATE_DESCRIPTIONS[state] ?? state;
  return (
    <span
      className={`state-pill ${stateClass}`}
      aria-label={`State: ${description}`}
      title={description}
    >
      <span aria-hidden style={{ fontSize: 9 }}>{STATE_GLYPHS[stateClass] ?? '?'}</span>
      {label ?? state}
    </span>
  );
}

/**
 * HostStatusPill maps the host status (online / offline / etc.)
 * onto the same pill styling. Online → running-coloured;
 * everything else → stopped.
 */
export function HostStatusPill({ status }: { status: string }) {
  const isOnline = status === 'online';
  return (
    <StatePill
      state={isOnline ? 'running' : 'stopped'}
      label={status}
    />
  );
}

/**
 * LockBadge surfaces a per-row migration lock — either an in-flight
 * migration on the source row or a rolled-back state on the dest
 * row after a failed migration. Was inline in VMs.tsx; promoted to
 * the shared components dir so HostDetail's VM-rows tab can reuse
 * it without reaching back into the page module.
 */
export function LockBadge({ reason }: { reason: LockReason }) {
  const isRollback = reason === 'rolled-back';
  const description = isRollback ? 'Rolled back after failed migration' : 'Migration in progress';
  return (
    <span
      className={`lock-pill ${isRollback ? 'danger' : 'warn'}`}
      aria-label={description}
      title={description}
    >
      <span aria-hidden style={{ marginRight: 4 }}>{isRollback ? '↺' : '↗'}</span>
      {isRollback ? 'rolled back' : 'migrating'}
    </span>
  );
}
