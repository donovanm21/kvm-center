import { useEffect, useState } from 'react';

import { Modal } from './Modal';
import { ErrorBanner, Field, TextInput } from './FormBits';
import { useAuth } from '@/hooks/useAuth';
import { useTOTPDisable, useTOTPEnable, useTOTPSetup, type TOTPSetupResponse } from '@/hooks/useTOTP';
import { useToast } from '@/hooks/useToast';

interface Props {
  open: boolean;
  onClose: () => void;
}

type Phase = 'overview' | 'enroll' | 'disable';

/**
 * Security modal — TOTP 2FA enrolment / disable for the current user.
 * The enrolment flow is: generate secret (server) → show it as a manual
 * entry key → verify with a live 6-digit code → server flips the flag.
 * Disable requires re-entering the account password so a stolen session
 * cookie alone can't strip 2FA off.
 */
export function SecurityModal({ open, onClose }: Props) {
  const { user, refresh } = useAuth();
  const { toast } = useToast();
  const [phase, setPhase] = useState<Phase>('overview');
  const [setupData, setSetupData] = useState<TOTPSetupResponse | null>(null);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [err, setErr] = useState<string | null>(null);

  const setup = useTOTPSetup();
  const enable = useTOTPEnable();
  const disable = useTOTPDisable();

  // Re-fetch the user record on each open so the enabled state is fresh.
  useEffect(() => {
    if (open) {
      setPhase('overview');
      setErr(null);
      setCode('');
      setPassword('');
      setSetupData(null);
      void refresh();
    }
  }, [open, refresh]);

  const totpOn = Boolean((user as { totp_enabled?: boolean } | null)?.totp_enabled);

  const startEnroll = async () => {
    setErr(null);
    try {
      const d = await setup.mutateAsync();
      setSetupData(d);
      setPhase('enroll');
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const confirmEnroll = async () => {
    setErr(null);
    if (code.trim().length !== 6) {
      setErr('Enter the 6-digit code.');
      return;
    }
    try {
      await enable.mutateAsync(code.trim());
      toast('success', 'Two-factor authentication enabled');
      await refresh();
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const confirmDisable = async () => {
    setErr(null);
    if (!password) {
      setErr('Password required.');
      return;
    }
    try {
      await disable.mutateAsync(password);
      toast('success', 'Two-factor authentication disabled');
      await refresh();
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  let footer: React.ReactNode = null;
  if (phase === 'overview') {
    footer = <button className="btn-secondary-sm" onClick={onClose}>Close</button>;
  } else if (phase === 'enroll') {
    footer = (
      <>
        <button className="btn-secondary-sm" onClick={onClose} disabled={enable.isPending}>Cancel</button>
        <button className="btn-primary-sm" onClick={confirmEnroll} disabled={enable.isPending}>
          {enable.isPending ? 'Verifying…' : 'Enable'}
        </button>
      </>
    );
  } else {
    footer = (
      <>
        <button className="btn-secondary-sm" onClick={() => setPhase('overview')} disabled={disable.isPending}>Back</button>
        <button className="btn-primary-sm" onClick={confirmDisable} disabled={disable.isPending}>
          {disable.isPending ? 'Disabling…' : 'Disable 2FA'}
        </button>
      </>
    );
  }

  return (
    <Modal open={open} onClose={onClose} title="Security" width={520} footer={footer}>
      {phase === 'overview' && (
        <div style={overviewCard}>
          <div>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)' }}>Two-factor authentication</div>
            <div style={{ fontSize: 11, color: 'var(--text-faded)', marginTop: 4, lineHeight: 1.5 }}>
              {totpOn
                ? 'Enabled — a 6-digit code from your authenticator app is required at sign-in.'
                : "Off — anyone with your password can sign in. Enable TOTP for an extra factor."}
            </div>
          </div>
          {totpOn ? (
            <button className="btn-secondary-sm" onClick={() => setPhase('disable')}>Disable</button>
          ) : (
            <button className="btn-primary-sm" onClick={startEnroll} disabled={setup.isPending}>
              {setup.isPending ? 'Preparing…' : 'Enable'}
            </button>
          )}
        </div>
      )}

      {phase === 'enroll' && setupData && (
        <>
          <ol style={{ margin: '0 0 0 20px', padding: 0, color: 'var(--text-body)', fontSize: 12, lineHeight: 1.6 }}>
            <li>Open your authenticator app (1Password, Authy, Google Authenticator, Bitwarden, iOS Passwords…).</li>
            <li>
              Add a new account and paste this key:
              <div style={secretKeyStyle}>{setupData.secret}</div>
              <div style={{ marginTop: 4, color: 'var(--text-faded)', fontSize: 11 }}>
                Issuer: {setupData.issuer} · Account: {setupData.account} · Algorithm: SHA-1 · 6 digits · 30 s
              </div>
            </li>
            <li>Enter the current 6-digit code to confirm:</li>
          </ol>
          <Field label="Authentication code">
            <TextInput
              autoFocus
              inputMode="numeric"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              style={{ letterSpacing: 4, textAlign: 'center', fontSize: 16, height: 40 }}
              placeholder="000000"
            />
          </Field>
          <ErrorBanner message={err} />
        </>
      )}

      {phase === 'disable' && (
        <>
          <p style={{ fontSize: 12, color: 'var(--text-body)' }}>
            Confirm your password to turn off 2FA. You can re-enrol later at any time.
          </p>
          <Field label="Password">
            <TextInput
              type="password"
              autoFocus
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <ErrorBanner message={err} />
        </>
      )}

      {phase === 'overview' && err && <ErrorBanner message={err} />}
    </Modal>
  );
}

const overviewCard: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 12,
  padding: 12,
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
};
const secretKeyStyle: React.CSSProperties = {
  marginTop: 6,
  padding: '8px 10px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  fontFamily: "'SF Mono', monospace",
  fontSize: 13,
  color: 'var(--text-strong)',
  wordBreak: 'break-all',
  userSelect: 'all',
};
