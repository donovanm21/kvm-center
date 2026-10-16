import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPut } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';

// Settings → Backup tab. Download and schedule backups here. Online restore
// is unavailable until a backend-aware transactional recovery flow exists.
// Keep all restore/restart mutations absent while the API is fail-closed.

interface BackupSchedule {
  enabled: boolean;
  every_hours?: number;
  cron_expr?: string;
  dest_path: string;
  retain_count: number;
  encryption_passphrase?: string;
  has_passphrase?: boolean; // server-only, response field
  last_run_at?: string;
  last_run_err?: string;
  last_run_size_bytes?: number;
  next_run_at?: string;
}

export function BackupTab() {
  const { hasPerm } = useAuth();
  const { toast } = useToast();
  const canManage = hasPerm('user.manage');

  const [busy, setBusy] = useState(false);
  const downloadBackup = async () => {
    setBusy(true);
    try {
      // The endpoint streams a tar.gz with a Content-Disposition
      // filename, so a plain anchor click does the right thing —
      // no JSON wrapping needed.
      const a = document.createElement('a');
      a.href = '/api/admin/backup';
      a.style.display = 'none';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      toast('success', 'Backup download started.');
    } finally {
      setTimeout(() => setBusy(false), 1500);
    }
  };

  // ── Schedule (v5.43.0) — periodic backup configuration. ───────
  const qc = useQueryClient();
  const schedQ = useQuery<BackupSchedule | null>({
    queryKey: ['backup-schedule'],
    queryFn: () => apiGet<BackupSchedule | null>('/api/admin/backup/schedule'),
  });
  const [schedDraft, setSchedDraft] = useState<BackupSchedule>({
    enabled: false,
    every_hours: 24,
    cron_expr: '',
    dest_path: '/var/lib/kvm-center/backups',
    retain_count: 7,
    encryption_passphrase: '',
  });
  const [schedMode, setSchedMode] = useState<'every' | 'cron'>('every');
  useEffect(() => {
    if (!schedQ.data) return;
    const hasCron = !!(schedQ.data.cron_expr && schedQ.data.cron_expr.trim());
    setSchedMode(hasCron ? 'cron' : 'every');
    setSchedDraft({
      enabled: schedQ.data.enabled,
      every_hours: schedQ.data.every_hours || 24,
      cron_expr: schedQ.data.cron_expr || '',
      dest_path: schedQ.data.dest_path || '/var/lib/kvm-center/backups',
      retain_count: schedQ.data.retain_count ?? 7,
      // Server never echoes the passphrase back. Empty in the form
      // means "keep current"; the user types only if they want to
      // change it.
      encryption_passphrase: '',
    });
  }, [schedQ.data]);
  const saveSched = useMutation({
    mutationFn: () => apiPut<BackupSchedule>('/api/admin/backup/schedule', schedDraft),
    onSuccess: () => {
      toast('success', schedDraft.enabled ? 'Schedule saved' : 'Schedule disabled');
      qc.invalidateQueries({ queryKey: ['backup-schedule'] });
    },
    onError: (e) => toast('error', `Save failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }} className="mobile-stack">
      <section className="panel" style={{ padding: 18 }}>
        <h3 style={titleStyle}>Backup</h3>
        <p style={paraStyle}>
          Streams a <code>.tar.gz</code> bundle with the full store snapshot,
          server TLS material, cluster CA, and the migration grant signer key.
          Pair with a copy of <code>.env</code> (Mongo password) for a
          complete disaster-recovery set — <code>.env</code> is intentionally
          not in the tarball so secrets aren't auto-distributed.
        </p>
        <button
          type="button"
          className="btn-primary-sm"
          disabled={!canManage || busy}
          onClick={downloadBackup}
          title={!canManage ? 'Requires user.manage' : 'Download a backup tarball now'}
        >
          {busy ? 'Preparing…' : 'Download backup'}
        </button>
      </section>

      <section className="panel" style={{ padding: 18 }}>
        <h3 style={titleStyle}>Restore</h3>
        <p id="restore-unavailable" style={paraStyle} role="status">
          Online restore is temporarily unavailable. It requires a backend-aware,
          transactional recovery workflow before it can safely apply a backup.
          Upload, validation, and restart-on-restore are disabled. Backup downloads
          and schedules remain available.
        </p>
        <p style={paraStyle}>
          For recovery, stop the service and follow the{' '}
          <a href="/docs/backup">offline recovery guidance</a> for your storage backend.
        </p>
        <fieldset disabled aria-describedby="restore-unavailable" style={{ border: 0, margin: 0, padding: 0 }}>
          <input
            type="file"
            accept=".tar.gz,.tgz,application/gzip"
            aria-label="Restore backup file"
            style={{ width: '100%', fontSize: 12, color: 'var(--text-body)', marginBottom: 10 }}
          />
          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" className="btn-danger-sm" disabled>
              Apply restore
            </button>
            <button type="button" className="btn-secondary-sm" disabled>
              Restart server
            </button>
          </div>
        </fieldset>
      </section>

      <section className="panel" style={{ padding: 18, gridColumn: '1 / -1' }}>
        <h3 style={titleStyle}>Schedule</h3>
        <p style={paraStyle}>
          When enabled, the server runs a backup every{' '}
          <code>every_hours</code> hours, writes the tarball to{' '}
          <code>dest_path</code>, and keeps the last{' '}
          <code>retain_count</code> files. Mount a host directory
          into the container so backups land outside the container's
          writable layer.
        </p>
        <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '10px 16px', alignItems: 'center', fontSize: 12 }}>
          <label>
            <input
              type="checkbox"
              checked={schedDraft.enabled}
              onChange={(e) => setSchedDraft({ ...schedDraft, enabled: e.target.checked })}
              disabled={!canManage}
            />{' '}
            Enable scheduled backups
          </label>
          <span />
          <label>Cadence</label>
          <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <input
                type="radio"
                checked={schedMode === 'every'}
                onChange={() => { setSchedMode('every'); setSchedDraft({ ...schedDraft, cron_expr: '' }); }}
                disabled={!canManage || !schedDraft.enabled}
              /> Every N hours
            </label>
            <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <input
                type="radio"
                checked={schedMode === 'cron'}
                onChange={() => setSchedMode('cron')}
                disabled={!canManage || !schedDraft.enabled}
              /> Cron expression
            </label>
          </div>
          {schedMode === 'every' ? (
            <>
              <label>Every (hours)</label>
              <input
                type="number"
                min={1}
                max={720}
                value={schedDraft.every_hours}
                onChange={(e) => setSchedDraft({ ...schedDraft, every_hours: parseInt(e.target.value || '0', 10) })}
                disabled={!canManage || !schedDraft.enabled}
                style={schedInputStyle}
              />
            </>
          ) : (
            <>
              <label>Cron (5 fields)</label>
              <input
                type="text"
                placeholder="0 3 * * *  (daily 03:00)"
                value={schedDraft.cron_expr}
                onChange={(e) => setSchedDraft({ ...schedDraft, cron_expr: e.target.value })}
                disabled={!canManage || !schedDraft.enabled}
                style={{ ...schedInputStyle, fontFamily: "'SF Mono', monospace" }}
              />
            </>
          )}
          <label>Destination path</label>
          <input
            type="text"
            value={schedDraft.dest_path}
            onChange={(e) => setSchedDraft({ ...schedDraft, dest_path: e.target.value })}
            disabled={!canManage || !schedDraft.enabled}
            placeholder="/var/lib/kvm-center/backups"
            style={schedInputStyle}
          />
          <label>Retain count</label>
          <input
            type="number"
            min={0}
            max={365}
            value={schedDraft.retain_count}
            onChange={(e) => setSchedDraft({ ...schedDraft, retain_count: parseInt(e.target.value || '0', 10) })}
            disabled={!canManage || !schedDraft.enabled}
            style={schedInputStyle}
          />
          <label>Encryption passphrase</label>
          <input
            type="password"
            placeholder={schedQ.data?.has_passphrase ? '(configured — leave blank to keep)' : '(optional — set to encrypt tarballs with AES-GCM)'}
            value={schedDraft.encryption_passphrase}
            onChange={(e) => setSchedDraft({ ...schedDraft, encryption_passphrase: e.target.value })}
            disabled={!canManage || !schedDraft.enabled}
            style={schedInputStyle}
          />
        </div>
        <p style={{ ...paraStyle, marginTop: 10, marginBottom: 0, fontSize: 11 }}>
          When set, tarballs land as <code>kvm-center-backup-…tar.gz.enc</code> wrapped in
          AES-256-GCM under a PBKDF2-stretched key. <strong>The server stores the passphrase
          alongside the schedule</strong> — protect <code>data.json</code> on disk
          accordingly. Restore prompts for it automatically when the magic header is detected.
        </p>
        <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
          <button
            type="button"
            className="btn-primary-sm"
            disabled={!canManage || saveSched.isPending}
            onClick={() => saveSched.mutate()}
          >
            {saveSched.isPending ? 'Saving…' : 'Save schedule'}
          </button>
        </div>
        {schedQ.data?.last_run_at && (
          <div style={{ marginTop: 14, padding: 10, background: 'var(--surface-2)', borderRadius: 4, fontSize: 12 }}>
            <div><strong>Last run</strong> — {new Date(schedQ.data.last_run_at).toLocaleString()}</div>
            {schedQ.data.last_run_err
              ? <div style={{ color: '#F85149', marginTop: 4 }}>error: {schedQ.data.last_run_err}</div>
              : <div style={{ color: 'var(--text-muted)', marginTop: 4 }}>
                  ok — {(schedQ.data.last_run_size_bytes || 0).toLocaleString()} bytes
                </div>}
            {schedQ.data.next_run_at && (
              <div style={{ color: 'var(--text-muted)', marginTop: 4 }}>
                Next run — {new Date(schedQ.data.next_run_at).toLocaleString()}
              </div>
            )}
          </div>
        )}
      </section>
    </div>
  );
}

const schedInputStyle: React.CSSProperties = {
  height: 30,
  padding: '0 10px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  color: 'var(--text-body)',
  fontSize: 12,
  fontFamily: 'inherit',
};

const titleStyle: React.CSSProperties = {
  margin: '0 0 10px 0',
  fontSize: 14,
  color: 'var(--text-strong)',
  fontWeight: 600,
};
const paraStyle: React.CSSProperties = {
  margin: '0 0 12px 0',
  fontSize: 12,
  color: 'var(--text-muted)',
  lineHeight: 1.55,
};
