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
          background: checked ? '#E3571E' : 'var(--surface-3)',
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
