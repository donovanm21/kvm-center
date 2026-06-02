import { useEffect, useState } from 'react';

import { type SSEStatus, getSSEStatus, subscribeSSEStatus } from '@/api/events';

// SSEStatusPill renders a tiny "live / reconnecting / disconnected"
// indicator in the top bar. Pre-v5.50 the EventSource's onerror
// only logged to the console; if the proxy buffered or auth lapsed
// the operator kept seeing 60-second-stale data with no signal that
// "live" was no longer live.
//
// "open" is the default success state and renders unobtrusively (a
// small green dot). "reconnecting" pulses amber. "closed" goes red
// — that's a terminal EventSource state usually meaning auth
// expired and a reload is needed.

const STATE_LABEL: Record<SSEStatus, string> = {
  connecting: 'connecting…',
  open: 'live',
  reconnecting: 'reconnecting…',
  closed: 'disconnected',
};

const STATE_TONE: Record<SSEStatus, { bg: string; fg: string; dot: string }> = {
  connecting:   { bg: 'rgba(125,140,160,0.12)', fg: 'var(--text-muted)',  dot: '#7d8ca0' },
  open:         { bg: 'rgba(63,185,80,0.12)',   fg: 'var(--green)',        dot: 'var(--green)' },
  reconnecting: { bg: 'rgba(227,140,30,0.14)',  fg: '#f59f5d',             dot: '#f59f5d' },
  closed:       { bg: 'rgba(248,81,73,0.14)',   fg: '#f85149',             dot: '#f85149' },
};

export function SSEStatusPill() {
  const [s, setS] = useState<SSEStatus>(getSSEStatus());
  useEffect(() => subscribeSSEStatus(setS), []);
  // Don't render anything in the steady "open" case — only surface
  // when state is degraded. Less visual noise on the topbar.
  if (s === 'open') return null;
  const tone = STATE_TONE[s];
  return (
    <span
      role="status"
      aria-label={`Live event stream is ${STATE_LABEL[s]}`}
      title={s === 'closed' ? 'Connection closed — reload the tab' : 'Live event stream temporarily unavailable'}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        padding: '3px 9px',
        borderRadius: 999,
        background: tone.bg,
        color: tone.fg,
        fontSize: 11,
        fontWeight: 600,
        letterSpacing: '0.02em',
      }}
    >
      <span
        aria-hidden
        style={{
          width: 7,
          height: 7,
          borderRadius: '50%',
          background: tone.dot,
          animation: s === 'reconnecting' ? 'kvmc-pulse 1.4s infinite ease-in-out' : 'none',
        }}
      />
      {STATE_LABEL[s]}
      <style>{`
        @keyframes kvmc-pulse {
          0%, 100% { opacity: 1; }
          50%      { opacity: 0.35; }
        }
      `}</style>
    </span>
  );
}
