import type { ReactNode } from 'react';

interface Props {
  page: number;
  totalPages: number;
  totalItems: number;
  perPage: number;
  /** Singular/plural noun rendered in the count line ("vms", "migrations"). */
  noun?: string;
  onChange: (page: number) => void;
}

/**
 * Numbered pagination footer used by the Dashboard, Migrations, and
 * Storage list views. Caller is responsible for slicing the list and
 * resetting page=1 when filters change.
 */
export function Pagination({ page, totalPages, totalItems, perPage, noun = 'rows', onChange }: Props) {
  const start = (page - 1) * perPage + 1;
  const end = Math.min(page * perPage, totalItems);
  const pages = Array.from({ length: totalPages }, (_, i) => i + 1);
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: 12 }}>
      <span style={{ fontSize: 11, color: 'var(--text-faded)' }}>
        Showing {start}–{end} of {totalItems} {noun}
      </span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <PageBtn disabled={page === 1} onClick={() => onChange(page - 1)}>‹</PageBtn>
        {pages.map((n) => (
          <PageBtn key={n} active={n === page} onClick={() => onChange(n)}>{n}</PageBtn>
        ))}
        <PageBtn disabled={page === totalPages} onClick={() => onChange(page + 1)}>›</PageBtn>
      </div>
    </div>
  );
}

function PageBtn({
  children, active = false, disabled = false, onClick,
}: { children: ReactNode; active?: boolean; disabled?: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={'page-btn' + (active ? ' active' : '')}
      style={{
        minWidth: 28, height: 28, padding: '0 8px',
        borderRadius: 4, fontSize: 12, fontFamily: 'inherit',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.4 : 1,
      }}
    >
      {children}
    </button>
  );
}
