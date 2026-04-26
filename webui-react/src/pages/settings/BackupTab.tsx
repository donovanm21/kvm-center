import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost, apiPostForm, apiPut } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { useToast } from '@/hooks/useToast';

// Settings → Backup tab (v5.39.0). Front end for /api/admin/backup
// (download) + /api/admin/restore (upload) + /api/admin/restart
// (post-restore reboot). Two-pane layout — left pane backs up, right
// pane restores. Restore is gated behind a typed-confirmation step
// because it stomps live state on the next restart.

interface BackupSchedule {
  enabled: boolean;
  every_hours: number;
  dest_path: string;
  retain_count: number;
  last_run_at?: string;
  last_run_err?: string;
  last_run_size_bytes?: number;
  next_run_at?: string;
}

interface BackupManifest {
  version: string;
  created_at: string;
  store_bytes: number;
  schema_format: string;
  files: Record<string, string>;
}

interface RestoreSummary {
  ok: boolean;
  manifest: BackupManifest;
  files_restored: string[];
  restart_required: boolean;
  staged_at: string;
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

  const fileRef = useRef<HTMLInputElement | null>(null);
  const [chosen, setChosen] = useState<File | null>(null);
  const [confirmText, setConfirmText] = useState('');
  const [summary, setSummary] = useState<RestoreSummary | null>(null);

  const restore = useMutation({
    mutationFn: async () => {
      if (!chosen) throw new Error('pick a backup tarball first');
      const fd = new FormData();
      fd.append('backup', chosen);
      return apiPostForm<RestoreSummary>('/api/admin/restore', fd);
    },
    onSuccess: (s) => {
      setSummary(s);
      toast('success', `Restored ${s.files_restored.length} file(s). Restart required.`);
    },
    onError: (e) => toast('error', `Restore failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const restart = useMutation({
    mutationFn: () => apiPost<{ ok: boolean; message: string }>('/api/admin/restart'),
    onSuccess: (r) => toast('success', r?.message || 'Server restarting…'),
    onError: (e) => toast('error', `Restart failed: ${e instanceof Error ? e.message : String(e)}`),
  });

  const confirmReady = confirmText.trim().toUpperCase() === 'RESTORE';

  // ── Schedule (v5.43.0) — periodic backup configuration. ───────
  const qc = useQueryClient();
  const schedQ = useQuery<BackupSchedule | null>({
    queryKey: ['backup-schedule'],
    queryFn: () => apiGet<BackupSchedule | null>('/api/admin/backup/schedule'),
  });
  const [schedDraft, setSchedDraft] = useState<BackupSchedule>({
    enabled: false,
    every_hours: 24,
    dest_path: '/var/lib/kvm-center/backups',
    retain_count: 7,
  });
  useEffect(() => {
    if (schedQ.data) setSchedDraft({
      enabled: schedQ.data.enabled,
      every_hours: schedQ.data.every_hours || 24,
      dest_path: schedQ.data.dest_path || '/var/lib/kvm-center/backups',
      retain_count: schedQ.data.retain_count ?? 7,
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
        <p style={paraStyle}>
          Upload a backup <code>.tar.gz</code> produced by the button on the
          left (or by the CLI export). Checksums are verified before any
          file is written. After a successful upload the server needs to
          restart to load the restored state — click <strong>Restart server</strong>{' '}
          when ready.
        </p>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 10 }}>
          <input
            ref={fileRef}
            type="file"
            accept=".tar.gz,.tgz,application/gzip"
            onChange={(e) => setChosen(e.target.files?.[0] ?? null)}
            style={{ flex: 1, fontSize: 12, color: 'var(--text-body)' }}
            disabled={!canManage || restore.isPending}
          />
        </div>
        <label style={{ display: 'block', fontSize: 11, color: 'var(--text-faded)', marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
          Type <code>RESTORE</code> to confirm
        </label>
        <input
          type="text"
          value={confirmText}
          onChange={(e) => setConfirmText(e.target.value)}
          style={{
            width: '100%', height: 32, padding: '0 10px',
            background: 'var(--bg-input)', border: '1px solid var(--border)',
            borderRadius: 4, color: 'var(--text-body)', fontSize: 13,
            fontFamily: "'SF Mono', monospace", marginBottom: 10,
          }}
          disabled={!canManage || restore.isPending}
        />
        <div style={{ display: 'flex', gap: 8 }}>
          <button
            type="button"
            className="btn-danger-sm"
            disabled={!canManage || restore.isPending || !chosen || !confirmReady}
            onClick={() => restore.mutate()}
          >
            {restore.isPending ? 'Restoring…' : 'Apply restore'}
          </button>
          <button
            type="button"
            className="btn-secondary-sm"
            disabled={!canManage || restart.isPending || !summary?.restart_required}
            onClick={() => restart.mutate()}
            title={summary?.restart_required ? 'Server will exit; container restart policy brings it back' : 'No restart pending'}
          >
            {restart.isPending ? 'Restarting…' : 'Restart server'}
          </button>
        </div>

        {summary && (
          <div style={{ marginTop: 14, padding: 10, background: 'var(--surface-2)', borderRadius: 4, fontSize: 12 }}>
            <div style={{ marginBottom: 4 }}>
              <strong>Restored from</strong> {summary.manifest.version}{' '}
              ({new Date(summary.manifest.created_at).toLocaleString()})
            </div>
            <div style={{ color: 'var(--text-muted)' }}>
              {summary.files_restored.length} file(s) placed:{' '}
              {summary.files_restored.map((f) => f.split('/').slice(-2).join('/')).join(', ')}
            </div>
          </div>
        )}
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
        </div>
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
