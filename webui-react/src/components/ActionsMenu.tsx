import { useEffect, useRef, useState, type ReactNode } from 'react';

export interface ActionItem {
  /** label rendered in the menu row */
  label: ReactNode;
  /** called on click; must be short-running (kick off an async task, show a modal, etc.) */
  onSelect?: () => void;
  /** when false the item is greyed out and unclickable; pair with `tooltip` to explain */
  enabled?: boolean;
  /** tooltip shown on hover — ideal for "Requires vm.snapshot" / "Disabled by administrator" */
  tooltip?: string;
  /** renders in red (confirm-destructive styling) */
  danger?: boolean;
}

export interface ActionGroup {
  items: ActionItem[];
}

/**
 * A right-aligned "Actions ▾" button whose menu mirrors the v3 dropdown
 * look (accent-subtle hover, red destructive items, inter-group separators).
 *
 * Consumers pass ActionGroup[]; each group is rendered with a separator
 * between them. Items are always visible — enabled=false greys them out
 * rather than hiding, because a hidden action is harder to discover than
 * a greyed one with a "Requires …" tooltip.
 */
export function ActionsMenu({ groups }: { groups: ActionGroup[] }) {
  const [open, setOpen] = useState(false);
  const [focusedIdx, setFocusedIdx] = useState(0);
  const wrap = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  // Flatten groups → indexable list for keyboard nav. Disabled items
  // are kept in the list but skipped over by Up/Down.
  const flat: ActionItem[] = groups.flatMap((g) => g.items);
  const enabledIdx = (start: number, dir: 1 | -1) => {
    if (flat.length === 0) return -1;
    let i = start;
    for (let k = 0; k < flat.length; k++) {
      i = (i + dir + flat.length) % flat.length;
      if (flat[i].enabled !== false) return i;
    }
    return -1;
  };

  useEffect(() => {
    if (!open) return;
    function onDocClick(e: MouseEvent) {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener('click', onDocClick);
    return () => document.removeEventListener('click', onDocClick);
  }, [open]);

  useEffect(() => {
    if (open) {
      const first = enabledIdx(-1, 1);
      setFocusedIdx(first >= 0 ? first : 0);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      setOpen(false);
      triggerRef.current?.focus();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      setFocusedIdx((i) => enabledIdx(i, 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setFocusedIdx((i) => enabledIdx(i, -1));
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      const it = flat[focusedIdx];
      if (it && it.enabled !== false) {
        setOpen(false);
        it.onSelect?.();
      }
    } else if (e.key === 'Home') {
      e.preventDefault();
      setFocusedIdx(enabledIdx(-1, 1));
    } else if (e.key === 'End') {
      e.preventDefault();
      setFocusedIdx(enabledIdx(flat.length, -1));
    }
  };

  let flatIdx = -1;

  return (
    <div ref={wrap} style={{ position: 'relative' }}>
      <button
        ref={triggerRef}
        type="button"
        className="btn-primary-sm"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if ((e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        Actions <Chevron />
      </button>
      {open && (
        <div role="menu" tabIndex={-1} style={menuStyle} onKeyDown={onMenuKeyDown}>
          {groups.map((g, gi) => (
            <div key={gi}>
              {gi > 0 && <div style={sepStyle} />}
              {g.items.map((it, ii) => {
                flatIdx++;
                const isFocused = flatIdx === focusedIdx;
                const myIdx = flatIdx;
                return (
                  <div
                    key={ii}
                    role="menuitem"
                    tabIndex={isFocused ? 0 : -1}
                    title={it.tooltip}
                    aria-disabled={it.enabled === false}
                    ref={(el) => {
                      if (isFocused && el) el.focus();
                    }}
                    onClick={() => {
                      if (it.enabled === false) return;
                      setOpen(false);
                      it.onSelect?.();
                    }}
                    onMouseEnter={(e) => {
                      setFocusedIdx(myIdx);
                      if (it.enabled === false) return;
                      (e.currentTarget as HTMLElement).style.background = it.danger ? '#3D1E1E' : 'var(--surface-2)';
                    }}
                    onMouseLeave={(e) => {
                      (e.currentTarget as HTMLElement).style.background = 'transparent';
                    }}
                    style={{
                      ...itemStyle,
                      ...(it.danger ? dangerStyle : {}),
                      ...(it.enabled === false ? disabledStyle : {}),
                      ...(isFocused && it.enabled !== false ? focusedItemStyle : {}),
                    }}
                  >
                    {it.label}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function Chevron() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" fill="currentColor" style={{ marginLeft: 4 }}>
      <path d="M1 3l4 4 4-4z" />
    </svg>
  );
}

const menuStyle: React.CSSProperties = {
  position: 'absolute',
  right: 0,
  top: 'calc(100% + 6px)',
  minWidth: 200,
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
  padding: '6px 0',
  zIndex: 200,
};
const itemStyle: React.CSSProperties = {
  padding: '8px 14px',
  fontSize: 12,
  color: 'var(--text-body)',
  cursor: 'pointer',
  whiteSpace: 'nowrap',
};
const dangerStyle: React.CSSProperties = { color: '#F85149' };
const disabledStyle: React.CSSProperties = { color: 'var(--text-disabled)', cursor: 'not-allowed', opacity: 0.55 };
const sepStyle: React.CSSProperties = { height: 1, background: 'var(--border)', margin: '6px 0' };
const focusedItemStyle: React.CSSProperties = { background: 'var(--surface-2)', outline: '2px solid var(--accent)', outlineOffset: -2 };
