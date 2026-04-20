import { useState, useMemo } from 'react';

// Shared sortable-header primitives. Drop-in for any <table className="data-table">:
// the <th> visual (uppercase, faded, no decoration) matches the static headers on
// pages like the Storage pools table. The sort indicator only appears on the
// actively-sorted column, so non-sortable headers look identical to sortable ones
// until the user clicks.
//
// Usage:
//   const sort = useSortable<VM>('name', 'asc');
//   const items = sort.apply(data);
//   <SortTh sort={sort} k="name">Name</SortTh>

export type SortDir = 'asc' | 'desc';

export function useSortable<T>(initialKey: keyof T & string, initialDir: SortDir = 'asc') {
  const [key, setKey] = useState<string>(initialKey);
  const [dir, setDir] = useState<SortDir>(initialDir);
  const toggle = (k: string) => {
    if (k === key) setDir(dir === 'asc' ? 'desc' : 'asc');
    else { setKey(k); setDir('asc'); }
  };
  const apply = <X extends Record<string, any>>(rows: X[]): X[] => {
    const out = [...rows];
    out.sort((a, b) => {
      const av = a[key];
      const bv = b[key];
      let cmp: number;
      if (typeof av === 'number' && typeof bv === 'number') cmp = av - bv;
      else cmp = String(av ?? '').localeCompare(String(bv ?? ''));
      return dir === 'asc' ? cmp : -cmp;
    });
    return out;
  };
  return useMemo(() => ({ key, dir, toggle, apply }), [key, dir]);
}

export function SortTh({
  sort, k, children, width,
}: {
  sort: { key: string; dir: SortDir; toggle: (k: string) => void };
  k: string;
  children: React.ReactNode;
  width?: number;
}) {
  const active = sort.key === k;
  return (
    <th
      onClick={() => sort.toggle(k)}
      style={{ cursor: 'pointer', userSelect: 'none', ...(width ? { width } : {}) }}
    >
      {children}
      <span
        aria-hidden
        style={{
          marginLeft: 6,
          fontSize: 9,
          visibility: active ? 'visible' : 'hidden',
          color: 'var(--text-muted)',
        }}
      >
        {sort.dir === 'asc' ? '▲' : '▼'}
      </span>
    </th>
  );
}
