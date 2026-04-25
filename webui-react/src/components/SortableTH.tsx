import { useCallback, useMemo, useState } from 'react';

export type SortDir = 'asc' | 'desc';
export interface SortState<K extends string> {
  key: K;
  dir: SortDir;
}

/**
 * Thin sort-state hook: tracks a (key, direction) pair and exposes a
 * toggle that cycles asc → desc → asc when the same header is clicked
 * twice, or snaps to the new header's default direction when switching.
 */
export function useSort<K extends string>(initialKey: K, initialDir: SortDir = 'asc'): {
  sort: SortState<K>;
  onToggle: (key: K) => void;
  compare: (a: unknown, b: unknown) => number;
} {
  const [sort, setSort] = useState<SortState<K>>({ key: initialKey, dir: initialDir });
  const onToggle = useCallback((key: K) => {
    setSort((cur) => (cur.key === key ? { key, dir: cur.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'asc' }));
  }, []);
  // Type-aware compare. Pre-v5.37 every non-numeric value coerced to
  // string, which broke (a) booleans (true/false sorted alphabetically:
  // false ahead of true on asc, regardless of intent), and (b) "small
  // integer carried as a string" cases like memory_mb sometimes
  // arriving as "8192" — those sorted lexically ("10" before "9").
  // Tier 2 #12, v5.37.0.
  const compare = useMemo(() => {
    const mul = sort.dir === 'asc' ? 1 : -1;
    return (a: unknown, b: unknown): number => {
      // Nulls / undefined go last regardless of direction so sorting
      // doesn't push empty cells to the visible top.
      const aMissing = a === null || a === undefined || a === '';
      const bMissing = b === null || b === undefined || b === '';
      if (aMissing && bMissing) return 0;
      if (aMissing) return 1;
      if (bMissing) return -1;

      if (typeof a === 'number' && typeof b === 'number') return (a - b) * mul;
      if (typeof a === 'boolean' && typeof b === 'boolean') {
        return (Number(a) - Number(b)) * mul;
      }
      // String pair that *looks* numeric on both sides → numeric
      // compare. Pure digits or signed/decimal literals only.
      if (typeof a === 'string' && typeof b === 'string') {
        const numLike = /^-?\d+(\.\d+)?$/;
        if (numLike.test(a) && numLike.test(b)) {
          return (parseFloat(a) - parseFloat(b)) * mul;
        }
      }
      const sa = String(a).toLowerCase();
      const sb = String(b).toLowerCase();
      return sa.localeCompare(sb) * mul;
    };
  }, [sort]);
  return { sort, onToggle, compare };
}

/**
 * <th> that shows a sort-direction indicator when active and dims its
 * chevron when inactive. Keeps the styling consistent with the v3 table
 * headers (all-caps, 10 px, 0.08 em letter-spacing — see globals.css).
 */
export function SortableTH<K extends string>({
  colKey,
  label,
  sort,
  onToggle,
  style,
  align = 'left',
}: {
  colKey: K;
  label: string;
  sort: SortState<K>;
  onToggle: (key: K) => void;
  style?: React.CSSProperties;
  align?: 'left' | 'right' | 'center';
}) {
  const active = sort.key === colKey;
  return (
    <th
      onClick={() => onToggle(colKey)}
      style={{
        cursor: 'pointer',
        userSelect: 'none',
        textAlign: align,
        ...style,
      }}
    >
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        {label}
        <Chevron dir={active ? sort.dir : null} />
      </span>
    </th>
  );
}

function Chevron({ dir }: { dir: SortDir | null }) {
  if (dir === null) {
    return (
      <svg width="9" height="9" viewBox="0 0 10 12" style={{ opacity: 0.25 }}>
        <path d="M5 0l4 4H1z" fill="currentColor" />
        <path d="M5 12l4-4H1z" fill="currentColor" />
      </svg>
    );
  }
  return (
    <svg width="9" height="9" viewBox="0 0 10 12">
      {dir === 'asc' ? (
        <path d="M5 0l4 5H1z" fill="var(--accent)" />
      ) : (
        <path d="M5 12l4-5H1z" fill="var(--accent)" />
      )}
    </svg>
  );
}
