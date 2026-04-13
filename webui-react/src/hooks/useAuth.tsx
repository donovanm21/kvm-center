import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

import { apiGet, apiPost, UnauthorizedError } from '@/api/client';
import type { CurrentUser, MeResponse } from '@/types';

interface AuthState {
  user: CurrentUser | null;
  permissions: string[];
  loading: boolean;
  hasPerm: (perm: string) => boolean;
  login: (username: string, password: string, totpCode?: string) => Promise<LoginResult>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

export type LoginResult =
  | { ok: true }
  | { ok: false; needsTOTP: true }
  | { ok: false; error: string };

const AuthCtx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const d = await apiGet<MeResponse>('/api/auth/me');
      setUser(d.user);
      setPermissions(Array.isArray(d.permissions) ? d.permissions : []);
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        setUser(null);
        setPermissions([]);
      } else {
        // Network errors: keep the previous state so transient blips
        // don't bounce the user to the login screen.
        console.error('auth refresh failed:', err);
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(async (username: string, password: string, totpCode?: string): Promise<LoginResult> => {
    try {
      const body: Record<string, string> = { username, password };
      if (totpCode) body.totp_code = totpCode;
      const r = await fetch('/api/auth/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const d = await r.json().catch(() => ({}));
      if (r.status === 202 && d.totp_required) {
        return { ok: false, needsTOTP: true };
      }
      if (!r.ok) return { ok: false, error: d.error || 'Login failed' };
      setUser(d.user);
      setPermissions(Array.isArray(d.permissions) ? d.permissions : []);
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : String(err) };
    }
  }, []);

  const logout = useCallback(async () => {
    try {
      await apiPost('/api/auth/logout');
    } catch {
      // ignore; local state reset is what matters
    }
    setUser(null);
    setPermissions([]);
  }, []);

  const hasPerm = useCallback(
    (perm: string) => permissions.includes('admin') || permissions.includes(perm),
    [permissions],
  );

  const value = useMemo<AuthState>(
    () => ({ user, permissions, loading, hasPerm, login, logout, refresh }),
    [user, permissions, loading, hasPerm, login, logout, refresh],
  );

  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>');
  return ctx;
}
