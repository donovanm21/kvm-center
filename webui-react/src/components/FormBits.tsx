import type { CSSProperties, ReactNode, InputHTMLAttributes, TextareaHTMLAttributes, SelectHTMLAttributes } from 'react';

/**
 * Tiny form primitives used across the Settings modals. Extracted to
 * cut the three tab components down to their actual logic instead of
 * drowning them in inline style props.
 */

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div style={{ marginTop: 10 }}>
      <label style={labelStyle}>{label}</label>
      {children}
      {hint && <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 4 }}>{hint}</div>}
    </div>
  );
}

export const TextInput = (props: InputHTMLAttributes<HTMLInputElement>) => (
  <input {...props} style={{ ...inputStyle, ...(props.style ?? {}) }} />
);

export const TextArea = (props: TextareaHTMLAttributes<HTMLTextAreaElement>) => (
  <textarea {...props} style={{ ...inputStyle, height: 'auto', minHeight: 80, padding: '8px 12px', ...(props.style ?? {}) }} />
);

export const Select = (props: SelectHTMLAttributes<HTMLSelectElement>) => (
  <select {...props} style={{ ...inputStyle, ...(props.style ?? {}) }} />
);

export function CheckList({
  items,
  selected,
  onToggle,
  empty,
  maxHeight = 150,
}: {
  items: { id: string; label: string; hint?: string }[];
  selected: string[];
  onToggle: (id: string) => void;
  empty: string;
  maxHeight?: number;
}) {
  if (items.length === 0) {
    return <div style={emptyListStyle}>{empty}</div>;
  }
  return (
    <div style={{ ...listStyle, maxHeight }}>
      {items.map((it) => (
        <label key={it.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0', cursor: 'pointer' }}>
          <input type="checkbox" checked={selected.includes(it.id)} onChange={() => onToggle(it.id)} />
          <span style={{ fontSize: 12, color: 'var(--text-body)' }}>
            {it.label}
            {it.hint && <span style={{ color: 'var(--text-faded)', marginLeft: 8, fontSize: 11 }}>{it.hint}</span>}
          </span>
        </label>
      ))}
    </div>
  );
}

export function ErrorBanner({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div className="tone-panel danger" style={{ marginTop: 10, padding: '8px 12px', borderRadius: 4, fontSize: 12 }}>
      {message}
    </div>
  );
}

const inputStyle: CSSProperties = {
  width: '100%',
  height: 34,
  padding: '0 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
  fontFamily: 'inherit',
  outline: 'none',
};
const labelStyle: CSSProperties = {
  display: 'block',
  fontSize: 11,
  color: 'var(--text-muted)',
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  marginBottom: 6,
};
const listStyle: CSSProperties = {
  overflowY: 'auto',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '8px 12px',
  background: 'var(--bg-input)',
};
const emptyListStyle: CSSProperties = {
  padding: '10px 12px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  fontSize: 11,
  color: 'var(--text-faded)',
};
