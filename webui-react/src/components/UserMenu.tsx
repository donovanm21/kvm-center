import { useEffect, useRef, useState } from 'react';

import { SecurityModal } from './SecurityModal';
import { useAuth } from '@/hooks/useAuth';

export function UserMenu() {
  const { user, hasPerm, logout } = useAuth();
  const [open, setOpen] = useState(false);
  const [security, setSecurity] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('click', onDoc);
    return () => document.removeEventListener('click', onDoc);
  }, [open]);

  if (!user) return null;

  const initials = (user.username || '?').slice(0, 2).toUpperCase();
  const roleLabel = hasPerm('admin') ? 'Administrator' : 'User';

  return (
    <div ref={ref} style={{ position: 'relative', flexShrink: 0 }}>
      <button
        type="button"
        aria-label="User menu"
        onClick={() => setOpen((v) => !v)}
        style={avatarStyle}
      >
        {initials}
      </button>
      {open && (
        <div style={menuStyle} role="menu">
          <div style={headerStyle}>
            <div style={{ fontWeight: 600, color: 'var(--text-strong)' }}>{user.username}</div>
            <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{roleLabel}</div>
          </div>
          <div
            role="menuitem"
            style={itemStyle}
            onClick={() => {
              setOpen(false);
              setSecurity(true);
            }}
          >
            <LockIcon /> Security &amp; 2FA
          </div>
          <div
            role="menuitem"
            style={{ ...itemStyle, color: '#F85149' }}
            onClick={() => {
              setOpen(false);
              void logout();
            }}
          >
            <SignOutIcon /> Sign out
          </div>
        </div>
      )}
      <SecurityModal open={security} onClose={() => setSecurity(false)} />
    </div>
  );
}

function LockIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
      <path d="M8 1a3.5 3.5 0 0 0-3.5 3.5V7H4a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V8a1 1 0 0 0-1-1h-.5V4.5A3.5 3.5 0 0 0 8 1zm2.5 6h-5V4.5a2.5 2.5 0 1 1 5 0V7z" />
    </svg>
  );
}
function SignOutIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
      <path d="M6 12.5a.5.5 0 0 0 .5.5h8a.5.5 0 0 0 .5-.5v-9a.5.5 0 0 0-.5-.5h-8a.5.5 0 0 0-.5.5v2a.5.5 0 0 1-1 0v-2A1.5 1.5 0 0 1 6.5 2h8A1.5 1.5 0 0 1 16 3.5v9a1.5 1.5 0 0 1-1.5 1.5h-8A1.5 1.5 0 0 1 5 12.5v-2a.5.5 0 0 1 1 0v2z" />
      <path d="M.146 8.354a.5.5 0 0 1 0-.708l3-3a.5.5 0 1 1 .708.708L1.707 7.5H10.5a.5.5 0 0 1 0 1H1.707l2.147 2.146a.5.5 0 0 1-.708.708l-3-3z" />
    </svg>
  );
}

const avatarStyle: React.CSSProperties = {
  width: 32,
  height: 32,
  borderRadius: '50%',
  border: 'none',
  background: 'linear-gradient(180deg, #F26419 0%, #E3571E 100%)',
  color: '#fff',
  fontSize: 11,
  fontWeight: 700,
  letterSpacing: '0.02em',
  cursor: 'pointer',
};
const menuStyle: React.CSSProperties = {
  position: 'absolute',
  right: 0,
  top: 40,
  minWidth: 220,
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
  zIndex: 200,
  padding: '6px 0',
};
const headerStyle: React.CSSProperties = {
  padding: '10px 14px 8px 14px',
  borderBottom: '1px solid var(--border)',
  marginBottom: 4,
  fontSize: 13,
};
const itemStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 10,
  padding: '8px 14px',
  fontSize: 12,
  color: 'var(--text-body)',
  cursor: 'pointer',
};
