import { useState, type FormEvent } from 'react';

import { BrandLogo } from '@/components/Brand';
import { ErrorBanner } from '@/components/FormBits';
import { useAuth } from '@/hooks/useAuth';

export function Login() {
  const { login, register, firstRun } = useAuth();
  const mode: 'register' | 'login' = firstRun ? 'register' : 'login';

  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [needsTOTP, setNeedsTOTP] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!username || !password) return;
    if (mode === 'register' && password.length < 8) {
      setError('Password must be at least 8 characters.');
      return;
    }
    setBusy(true);
    setError(null);
    const res = mode === 'register'
      ? await register(username, password, email || undefined)
      : await login(username, password, needsTOTP ? totp : undefined);
    setBusy(false);
    if (res.ok) return;
    if ('needsTOTP' in res && res.needsTOTP) {
      setNeedsTOTP(true);
      return;
    }
    if ('error' in res) setError(res.error);
  }

  return (
    <div className="auth-screen">
      <div className="auth-card">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <BrandLogo size={32} />
          <span className="brand">
            <span className="primary">kvm</span>
            <span className="accent">center</span>
          </span>
        </div>
        <div className="auth-title">{mode === 'register' ? 'Create Admin Account' : 'Sign in'}</div>
        <div className="auth-sub">
          {mode === 'register'
            ? 'No users exist yet. Create the first administrator account to start managing your cluster.'
            : 'Enter your credentials to manage the KVM Center cluster.'}
        </div>
        <form className="auth-form" onSubmit={onSubmit}>
          <label htmlFor="login-username">Username</label>
          <input
            id="login-username"
            autoComplete="username"
            autoFocus
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          {mode === 'register' && (
            <>
              <label htmlFor="login-email">Email (optional)</label>
              <input
                id="login-email"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </>
          )}
          <label htmlFor="login-password">
            Password{mode === 'register' ? ' (min 8 characters)' : ''}
          </label>
          <input
            id="login-password"
            type="password"
            autoComplete={mode === 'register' ? 'new-password' : 'current-password'}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {needsTOTP && (
            <>
              <label htmlFor="login-totp">Authentication code</label>
              <input
                id="login-totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                pattern="[0-9]{6}"
                placeholder="6-digit code"
                required
                autoFocus
                value={totp}
                onChange={(e) => setTotp(e.target.value)}
              />
            </>
          )}
          <ErrorBanner message={error} />
          <button type="submit" className="auth-submit" disabled={busy}>
            {busy
              ? mode === 'register' ? 'Creating…' : 'Signing in…'
              : mode === 'register'
                ? 'Create Admin & Sign In'
                : needsTOTP ? 'Verify & Sign In' : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  );
}
