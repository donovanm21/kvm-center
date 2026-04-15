import { useEffect, useMemo, useState } from 'react';

/**
 * Standard pagination primitive used by every long-list table in the
 * UI. Defaults to 25 rows/page with a 10/25/50/100/All selector.
 *
 *     const pg = usePagination(sortedRows);
 *     return (
 *       <>
 *         <div className="data-table-scroll">
 *           <table className="data-table">…{pg.items.map(…)}…</table>
 *         </div>
 *         <PaginationBar pagination={pg} noun="VMs" />
 *       </>
 *     );
 */

export const DEFAULT_PAGE_SIZE: PageSize = 25;
export const PAGE_SIZE_OPTIONS: PageSize[] = [10, 25, 50, 100, 'all'];

export type PageSize = 10 | 25 | 50 | 100 | 'all';

export interface Pagination<T> {
  items: T[];         // sliced for the current page
  total: number;
  page: number;       // 1-based
  pageCount: number;
  pageSize: PageSize;
  setPage: (p: number) => void;
  setPageSize: (s: PageSize) => void;
}

export function usePagination<T>(source: readonly T[], defaultSize: PageSize = DEFAULT_PAGE_SIZE): Pagination<T> {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<PageSize>(defaultSize);

  const total = source.length;
  const pageCount = useMemo(() => {
    if (pageSize === 'all') return 1;
    return Math.max(1, Math.ceil(total / pageSize));
  }, [total, pageSize]);

  // Clamp the current page when the underlying list or page size shrinks.
  useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);

  const items = useMemo(() => {
    if (pageSize === 'all') return source.slice();
    const start = (page - 1) * pageSize;
    return source.slice(start, start + pageSize);
  }, [source, page, pageSize]);

  return { items, total, page, pageCount, pageSize, setPage, setPageSize };
}

interface PaginationBarProps {
  pagination: Pagination<unknown>;
  noun?: string;
}

export function PaginationBar({ pagination, noun = 'rows' }: PaginationBarProps) {
  const { page, pageCount, pageSize, total, setPage, setPageSize } = pagination;
  if (total === 0) return null;

  const firstOnPage = pageSize === 'all' ? 1 : Math.min(total, (page - 1) * pageSize + 1);
  const lastOnPage = pageSize === 'all' ? total : Math.min(total, page * pageSize);

  return (
    <div className="data-pagination">
      <span>
        {firstOnPage}–{lastOnPage} of <span className="current">{total}</span> {noun}
      </span>
      <div className="spacer" />
      <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <span>Rows</span>
        <select
          value={String(pageSize)}
          onChange={(e) => {
            const v = e.target.value;
            setPageSize(v === 'all' ? 'all' : (Number(v) as PageSize));
            setPage(1);
          }}
        >
          {PAGE_SIZE_OPTIONS.map((o) => (
            <option key={String(o)} value={String(o)}>{o === 'all' ? 'All' : o}</option>
          ))}
        </select>
      </label>
      <button type="button" onClick={() => setPage(1)} disabled={page === 1} title="First page">«</button>
      <button type="button" onClick={() => setPage(page - 1)} disabled={page === 1} title="Previous">‹</button>
      <span className="current">{page} / {pageCount}</span>
      <button type="button" onClick={() => setPage(page + 1)} disabled={page >= pageCount} title="Next">›</button>
      <button type="button" onClick={() => setPage(pageCount)} disabled={page >= pageCount} title="Last page">»</button>
    </div>
  );
}

/* ── Legacy controlled-pagination shim ─────────────────────────
   The Dashboard / Migrations / Storage pages were built before
   usePagination existed and drive the page number from their own
   state. Keep them working by re-exporting a thin controlled
   component with the old prop surface. New callers should prefer
   usePagination + PaginationBar. */

interface LegacyProps {
  page: number;
  totalPages: number;
  totalItems: number;
  perPage: number;
  noun?: string;
  onChange: (page: number) => void;
}

export function Pagination({ page, totalPages, totalItems, perPage, noun = 'rows', onChange }: LegacyProps) {
  if (totalItems === 0) return null;
  const first = Math.min(totalItems, (page - 1) * perPage + 1);
  const last = Math.min(totalItems, page * perPage);
  return (
    <div className="data-pagination">
      <span>
        {first}–{last} of <span className="current">{totalItems}</span> {noun}
      </span>
      <div className="spacer" />
      <button type="button" onClick={() => onChange(1)} disabled={page === 1} title="First page">«</button>
      <button type="button" onClick={() => onChange(page - 1)} disabled={page === 1} title="Previous">‹</button>
      <span className="current">{page} / {totalPages}</span>
      <button type="button" onClick={() => onChange(page + 1)} disabled={page >= totalPages} title="Next">›</button>
      <button type="button" onClick={() => onChange(totalPages)} disabled={page >= totalPages} title="Last page">»</button>
    </div>
  );
}
