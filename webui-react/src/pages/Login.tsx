import { useState, type FormEvent } from 'react';

import { BrandLogo } from '@/components/Brand';
import { useAuth } from '@/hooks/useAuth';

export function Login() {
  const { login } = useAuth();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [needsTOTP, setNeedsTOTP] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!username || !password) return;
    setBusy(true);
    setError(null);
    const res = await login(username, password, needsTOTP ? totp : undefined);
    setBusy(false);
    if (res.ok) return; // AuthProvider flipped state; App re-renders into main shell
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
        <div className="auth-title">Sign in</div>
        <div className="auth-sub">Enter your credentials to manage the KVM Center cluster.</div>
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
          <label htmlFor="login-password">Password</label>
          <input
            id="login-password"
            type="password"
            autoComplete="current-password"
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
          {error && <div className="auth-error">{error}</div>}
          <button type="submit" className="auth-submit" disabled={busy}>
            {busy ? 'Signing in…' : needsTOTP ? 'Verify & Sign In' : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  );
}
