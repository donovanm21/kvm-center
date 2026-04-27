import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';

export type ToastKind = 'success' | 'error' | 'info';
interface Toast { id: number; kind: ToastKind; message: string; }

interface ToastCtx {
  toast: (kind: ToastKind, message: string) => void;
}

const Ctx = createContext<ToastCtx | null>(null);

let nextId = 1;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [queue, setQueue] = useState<Toast[]>([]);

  const toast = useCallback((kind: ToastKind, message: string) => {
    const ttl = kind === 'error' ? 7000 : 3500;
    setQueue((q) => {
      // Dedup: if the same kind+message is already showing, drop the
      // duplicate. A flapping API endpoint stacking 20 identical
      // error toasts on top of each other was real and obscured
      // anything else on screen.
      if (q.some((t) => t.kind === kind && t.message === message)) {
        return q;
      }
      const id = nextId++;
      setTimeout(() => setQueue((q2) => q2.filter((t) => t.id !== id)), ttl);
      return [...q, { id, kind, message }];
    });
  }, []);

  const value = useMemo(() => ({ toast }), [toast]);

  return (
    <Ctx.Provider value={value}>
      {children}
      <div
        style={{
          position: 'fixed',
          right: 16,
          bottom: 16,
          display: 'flex',
          flexDirection: 'column',
          gap: 8,
          zIndex: 2000,
          maxWidth: 420,
        }}
      >
        {queue.map((t) => (
          <div
            key={t.id}
            style={{
              padding: '10px 14px',
              borderRadius: 4,
              fontSize: 12,
              borderLeft: '3px solid',
              borderLeftColor: colorFor(t.kind),
              background: bgFor(t.kind),
              color: textFor(t.kind),
              boxShadow: '0 4px 16px rgba(0,0,0,0.35)',
              animation: 'toast-in 0.15s ease',
            }}
          >
            {t.message}
          </div>
        ))}
      </div>
      <style>{`@keyframes toast-in { from { transform: translateX(30px); opacity: 0; } to { transform: none; opacity: 1; } }`}</style>
    </Ctx.Provider>
  );
}

export function useToast() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useToast must be used inside <ToastProvider>');
  return ctx;
}

function colorFor(k: ToastKind) { return k === 'success' ? '#3FB950' : k === 'error' ? '#F85149' : '#E3571E'; }
function bgFor(k: ToastKind) { return k === 'success' ? '#1E3D23' : k === 'error' ? '#3D1E1E' : '#161B22'; }
function textFor(k: ToastKind) { return k === 'success' ? '#7EE787' : k === 'error' ? '#FFB3B3' : '#C9D1D9'; }
