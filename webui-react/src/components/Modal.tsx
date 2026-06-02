import { useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  width?: number;
  children: ReactNode;
  footer?: ReactNode;
}

/**
 * Body-portalled modal with backdrop click-to-dismiss + Escape key.
 * Portal ensures the modal DOM lives outside any scrollable parent and
 * can't be wiped by a parent re-render (the v3 lesson we keep learning).
 */
export function Modal({ open, onClose, title, width = 520, children, footer }: ModalProps) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;
  return createPortal(
    <div style={backdrop} onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div role="dialog" aria-modal="true" style={{ ...card, width }}>
        {title && <h3 style={titleStyle}>{title}</h3>}
        <div>{children}</div>
        {footer && <div style={footerStyle}>{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

const backdrop: React.CSSProperties = {
  position: 'fixed',
  inset: 0,
  background: 'rgba(0,0,0,0.55)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  zIndex: 1000,
};
const card: React.CSSProperties = {
  maxWidth: 'calc(100vw - 32px)',
  maxHeight: '90vh',
  overflowY: 'auto',
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 8,
  padding: '22px 24px',
  boxShadow: '0 20px 48px rgba(0,0,0,0.55)',
};
const titleStyle: React.CSSProperties = {
  margin: '0 0 12px 0',
  fontSize: 15,
  fontWeight: 600,
  color: 'var(--text-strong)',
};
const footerStyle: React.CSSProperties = {
  marginTop: 16,
  display: 'flex',
  justifyContent: 'flex-end',
  gap: 8,
};
