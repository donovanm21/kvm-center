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
  const compare = useMemo(() => {
    const mul = sort.dir === 'asc' ? 1 : -1;
    return (a: unknown, b: unknown): number => {
      if (typeof a === 'number' && typeof b === 'number') return (a - b) * mul;
      const sa = String(a ?? '').toLowerCase();
      const sb = String(b ?? '').toLowerCase();
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
