import { createContext, useCallback, useContext, useState, type ReactNode } from 'react';

import { Modal } from '@/components/Modal';

// useConfirm() replaces native window.confirm() calls. window.confirm
// has three problems we hit in practice:
//   - Safari (and any iframe context with blocked dialogs) silently
//     swallows the dialog, so the destructive action proceeds
//     unconfirmed.
//   - It's unstyleable — looks unbranded next to the styled <Modal>
//     used for every other dialog.
//   - No focus trap, no aria-labelledby, no keyboard-first UX.
//
// Returned function is async and resolves to a boolean: true if the
// operator clicks Confirm, false on Cancel / backdrop / Escape.
//
// Usage:
//   const confirm = useConfirm();
//   const ok = await confirm({ title: 'Delete VM?', body: 'This will…' });
//   if (ok) deleteMut.mutate(...);

interface ConfirmOptions {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Renders the confirm button in the danger tone. Default true for
   * destructive actions; pass false for benign confirmations. */
  destructive?: boolean;
}

type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFn | null>(null);

interface PendingConfirm extends ConfirmOptions {
  resolve: (ok: boolean) => void;
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<PendingConfirm | null>(null);

  const confirm = useCallback<ConfirmFn>((opts) => {
    return new Promise<boolean>((resolve) => {
      setPending({ ...opts, resolve });
    });
  }, []);

  const close = (ok: boolean) => {
    if (!pending) return;
    pending.resolve(ok);
    setPending(null);
  };

  const destructive = pending?.destructive ?? true;

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {pending && (
        <Modal
          open
          title={pending.title}
          onClose={() => close(false)}
          width={460}
          footer={
            <>
              <button onClick={() => close(false)}>{pending.cancelLabel ?? 'Cancel'}</button>
              <button
                className={destructive ? 'btn-danger' : 'btn-primary-sm'}
                onClick={() => close(true)}
                autoFocus
              >
                {pending.confirmLabel ?? (destructive ? 'Delete' : 'Confirm')}
              </button>
            </>
          }
        >
          {pending.body && <div style={{ fontSize: 14, color: 'var(--text-body)' }}>{pending.body}</div>}
        </Modal>
      )}
    </ConfirmContext.Provider>
  );
}

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  if (!ctx) {
    throw new Error('useConfirm() requires <ConfirmProvider> in the tree (mounted in App.tsx)');
  }
  return ctx;
}
