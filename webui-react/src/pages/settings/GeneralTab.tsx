import { useEffect, useState } from 'react';
import { useFeatures } from '@/hooks/useInventory';
import { useAuth } from '@/hooks/useAuth';
import { useUpdateFeatures } from '@/hooks/useAdminResources';
import type { Features } from '@/types';

interface Row {
  key: keyof Features;
  label: string;
  help: string;
}

// Each row toggles a *disable_…* flag. The UI shows positive polarity
// ("on = feature available") and inverts when writing.
const ROWS: Row[] = [
  { key: 'disable_vm_create',      label: 'Create VMs',         help: 'Allow operators to provision new VMs through the wizard.' },
  { key: 'disable_vm_delete',      label: 'Delete VMs',         help: 'Allow operators to permanently destroy VM domains and their disks.' },
  { key: 'disable_snapshots',      label: 'Snapshots',          help: 'Create, revert, and delete libvirt domain snapshots.' },
  { key: 'disable_console',        label: 'Remote console',     help: 'Hide the Launch Console button and the live thumbnail click-through.' },
  { key: 'disable_migrations',     label: 'VM migrations',      help: 'Live and offline migration between hypervisors.' },
  { key: 'disable_storage_upload', label: 'Storage uploads',    help: 'Upload ISOs and disk images to storage pools.' },
  { key: 'disable_storage_delete', label: 'Storage file delete',help: 'Delete files from storage pools through the UI.' },
];

export function GeneralTab() {
  const features = useFeatures();
  const { hasPerm } = useAuth();
  const update = useUpdateFeatures();
  const canEdit = hasPerm('user.manage');

  const toggle = (key: keyof Features, enabled: boolean) => {
    const next: Features = { ...(features.data ?? {}), [key]: !enabled };
    update.mutate(next);
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <PublicURLSection canEdit={canEdit} />

      <div style={introStyle}>
        Features are enabled by default. Disabling one here greys the corresponding buttons out
        for every user and the server rejects the underlying API call — a defense-in-depth
        against accidental operations.
        {!canEdit && (
          <>
            <br />
            <strong style={{ color: 'var(--text-faded)' }}>
              Read-only — requires the user.manage permission to edit.
            </strong>
          </>
        )}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {ROWS.map((r) => {
          const on = !features.data?.[r.key];
          return (
            <div key={r.key} style={rowStyle}>
              <div>
                <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)' }}>{r.label}</div>
                <div style={helpStyle}>{r.help}</div>
              </div>
              <Toggle checked={on} disabled={!canEdit || update.isPending} onChange={(v) => toggle(r.key, v)} />
            </div>
          );
        })}
        {/* v5.44.0 — positive-polarity flag, distinct from the
             disable_* family above. */}
        <div style={rowStyle}>
          <div>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)' }}>Strict SSH host-key checking</div>
            <div style={helpStyle}>
              Forces the agent's SSH-transport migration paths (offline scp, fallback live qemu+ssh) to dial
              with <code>StrictHostKeyChecking=yes</code>. Operators must populate{' '}
              <code>/var/lib/kvm-migrator/.ssh/known_hosts</code> on every agent first, otherwise SSH
              migrations refuse with "host key verification failed". Recommended on shared management
              networks; the default off matches the legacy posture audit-logged in v5.37.1.
            </div>
          </div>
          <Toggle
            checked={!!features.data?.ssh_strict_host_key_checking}
            disabled={!canEdit || update.isPending}
            onChange={(v) => {
              const next: Features = { ...(features.data ?? {}), ssh_strict_host_key_checking: v };
              update.mutate(next);
            }}
          />
        </div>
      </div>
    </div>
  );
}

// PublicURLSection persists Features.PublicURL, the canonical address
// clients use to reach this server. Feeds the Deployment page install
// snippets and notification webhook deep links. Empty falls back to
// window.location / KVM_CENTER_PUBLIC_URL env.
function PublicURLSection({ canEdit }: { canEdit: boolean }) {
  const [value, setValue] = useState('');
  const [saved, setSaved] = useState('');
  const [status, setStatus] = useState<'idle' | 'saving' | 'ok' | 'err'>('idle');
  const [errMsg, setErrMsg] = useState('');

  useEffect(() => {
    fetch('/api/setup/state')
      .then(r => r.json())
      .then(d => {
        const u = (d?.public_url || '') as string;
        setValue(u);
        setSaved(u);
      })
      .catch(() => {});
  }, []);

  const save = async () => {
    setStatus('saving');
    setErrMsg('');
    try {
      const r = await fetch('/api/settings/public-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ public_url: value.trim() }),
      });
      if (!r.ok) {
        const j = await r.json().catch(() => ({}));
        setStatus('err');
        setErrMsg(j.error || `HTTP ${r.status}`);
        return;
      }
      const j = await r.json();
      setValue(j.public_url || '');
      setSaved(j.public_url || '');
      setStatus('ok');
      setTimeout(() => setStatus('idle'), 2000);
    } catch (e: any) {
      setStatus('err');
      setErrMsg(String(e?.message || e));
    }
  };

  const dirty = value.trim().replace(/\/$/, '') !== saved.replace(/\/$/, '');

  return (
    <div style={{ marginBottom: 22, padding: '14px 16px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 6 }}>
      <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-strong)', marginBottom: 4 }}>
        Public URL
      </div>
      <div style={{ ...helpStyle, marginBottom: 10, maxWidth: 620 }}>
        Canonical address clients use to reach this server (e.g. <code>https://kvm.example.com</code>).
        Threads into Deployment page install snippets and notification webhook deep links.
        Leave blank to fall back to the browser's current URL.
      </div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <input
          type="text"
          value={value}
          onChange={e => setValue(e.target.value)}
          placeholder="https://kvm.example.com"
          disabled={!canEdit}
          style={{ flex: 1, minWidth: 260, padding: '6px 10px', fontSize: 13, background: 'var(--surface-2)', color: 'var(--text-strong)', border: '1px solid var(--border)', borderRadius: 4 }}
        />
        <button
          onClick={save}
          disabled={!canEdit || !dirty || status === 'saving'}
          style={{ padding: '6px 14px', fontSize: 12, fontWeight: 600, background: 'var(--accent)', color: '#fff', border: 'none', borderRadius: 4, cursor: (!canEdit || !dirty) ? 'not-allowed' : 'pointer', opacity: (!canEdit || !dirty) ? 0.5 : 1 }}
        >
          {status === 'saving' ? 'Saving…' : 'Save'}
        </button>
        {status === 'ok' && <span style={{ fontSize: 12, color: '#3FB950' }}>Saved</span>}
        {status === 'err' && <span style={{ fontSize: 12, color: '#F85149' }}>{errMsg}</span>}
      </div>
    </div>
  );
}

function Toggle({ checked, disabled, onChange }: { checked: boolean; disabled?: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ position: 'relative', display: 'inline-block', width: 42, height: 22, flexShrink: 0, opacity: disabled ? 0.5 : 1 }}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        style={{ opacity: 0, width: 0, height: 0 }}
      />
      <span
        style={{
          position: 'absolute',
          inset: 0,
          background: checked ? 'var(--accent)' : 'var(--surface-3)',
          borderRadius: 22,
          transition: 'background 0.18s',
          cursor: disabled ? 'not-allowed' : 'pointer',
        }}
      />
      <span
        style={{
          position: 'absolute',
          left: 2,
          top: 2,
          width: 18,
          height: 18,
          borderRadius: '50%',
          background: '#F0F6FC',
          transition: 'transform 0.18s',
          transform: checked ? 'translateX(20px)' : 'none',
        }}
      />
    </label>
  );
}

const introStyle: React.CSSProperties = {
  fontSize: 12,
  color: 'var(--text-muted)',
  marginBottom: 14,
  lineHeight: 1.5,
};
const rowStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 24,
  padding: '14px 16px',
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 6,
};
const helpStyle: React.CSSProperties = {
  fontSize: 11,
  color: 'var(--text-muted)',
  marginTop: 2,
  lineHeight: 1.4,
  maxWidth: 440,
};
