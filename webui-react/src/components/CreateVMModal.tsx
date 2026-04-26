import { useEffect, useMemo, useState } from 'react';
import { z } from 'zod';

import { ErrorBanner, Field, Select, TextInput } from '@/components/FormBits';
import { Modal } from '@/components/Modal';
import { diskGBSchema, memMBSchema, vcpuSchema, vmNameSchema } from '@/forms/schemas';
import { useHosts } from '@/hooks/useInventory';
import { useCreateVM, useVMCreateInfo, type VMCreatePayload } from '@/hooks/useVMCreate';

// Zod schema covering every field that's user-editable in the
// wizard. Centralised here so a tweak to "what's a valid VM name"
// lands once across snapshot creation, hardware edits, and the
// wizard. (v5.42.0)
const createVMSchema = z.object({
  name: vmNameSchema,
  memory: memMBSchema,
  vcpus: vcpuSchema,
  diskSize: diskGBSchema,
  source: z.enum(['iso', 'import', 'pxe']),
  isoPath: z.string(),
  importDisk: z.string(),
}).superRefine((v, ctx) => {
  if (v.source === 'iso' && !v.isoPath) {
    ctx.addIssue({ code: 'custom', path: ['isoPath'], message: 'Pick an ISO or switch install source.' });
  }
  if (v.source === 'import' && !v.importDisk.trim()) {
    ctx.addIssue({ code: 'custom', path: ['importDisk'], message: 'Provide the path to the existing disk image.' });
  }
});

interface Props {
  open: boolean;
  onClose: () => void;
}

type InstallSource = 'iso' | 'import' | 'pxe';

const DEFAULT_POOL_PATH = '/var/lib/libvirt/images';

/**
 * Create VM wizard. Everything lives in one scrolling form — matches
 * v3's single-screen layout rather than a multi-page wizard. The host
 * picker at the top is the primary switch: changing it refetches the
 * per-host create-info (ISOs / bridges / pools / os-variants) so the
 * dropdowns always reflect what the agent can see on its filesystem.
 */
export function CreateVMModal({ open, onClose }: Props) {
  const hosts = useHosts();
  const onlineHosts = useMemo(
    () => (hosts.data ?? []).filter((h) => h.status === 'online'),
    [hosts.data],
  );

  const [hostID, setHostID] = useState<string>('');
  useEffect(() => {
    if (!open) return;
    if (!hostID && onlineHosts.length > 0) setHostID(onlineHosts[0].id);
  }, [open, onlineHosts, hostID]);

  const info = useVMCreateInfo(hostID || null);
  const create = useCreateVM(hostID || null);

  const [name, setName] = useState('');
  const [memory, setMemory] = useState(2048);
  const [vcpus, setVCPUs] = useState(2);
  const [diskSize, setDiskSize] = useState(20);
  const [diskFormat, setDiskFormat] = useState<'qcow2' | 'raw'>('qcow2');
  const [firmware, setFirmware] = useState<'bios' | 'efi'>('bios');
  const [autostart, setAutostart] = useState(false);
  const [osVariant, setOsVariant] = useState('');
  const [bridge, setBridge] = useState('');
  const [poolPath, setPoolPath] = useState(DEFAULT_POOL_PATH);
  const [source, setSource] = useState<InstallSource>('iso');
  const [isoPath, setISOPath] = useState('');
  const [importDisk, setImportDisk] = useState('');
  const [err, setErr] = useState<string | null>(null);

  // When info finishes loading, pre-fill bridge/pool/iso defaults with
  // whatever the agent reported so the form is usable on the first click.
  useEffect(() => {
    if (!info.data) return;
    if (!bridge && info.data.bridges.length > 0) setBridge(info.data.bridges[0]);
    if (poolPath === DEFAULT_POOL_PATH && info.data.pools.length > 0) {
      setPoolPath(info.data.pools[0].path);
    }
    if (!isoPath && source === 'iso' && info.data.isos.length > 0) {
      setISOPath(info.data.isos[0]);
    }
    // Intentionally exclude the setters from the dep list — we only want
    // this to run when fresh info arrives, not on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info.data]);

  const reset = () => {
    // Reset hostID too — the open-effect re-picks the first online host
    // on next open. Pre-v5.37 the picker held a stale host (possibly
    // since-offline), which silently re-fired the host-info query
    // against an unreachable agent and surfaced the "info loading…"
    // state forever. Tier 2 #13.
    setHostID('');
    setName('');
    setMemory(2048);
    setVCPUs(2);
    setDiskSize(20);
    setDiskFormat('qcow2');
    setFirmware('bios');
    setAutostart(false);
    setOsVariant('');
    setBridge('');
    setPoolPath(DEFAULT_POOL_PATH);
    setSource('iso');
    setISOPath('');
    setImportDisk('');
    setErr(null);
  };
  const close = () => {
    reset();
    onClose();
  };

  const submit = async () => {
    setErr(null);
    if (!hostID) {
      setErr('Pick a host first.');
      return;
    }
    // Run Zod validation against the shared schemas. First failure's
    // message becomes the error banner; refinement-level issues
    // (ISO required when source=iso etc.) report through the same path.
    const parsed = createVMSchema.safeParse({
      name: name.trim(),
      memory,
      vcpus,
      diskSize: source === 'import' ? 1 : diskSize, // import path skips disk size; satisfy schema
      source,
      isoPath,
      importDisk,
    });
    if (!parsed.success) {
      setErr(parsed.error.issues[0]?.message ?? 'Form validation failed.');
      return;
    }

    const payload: VMCreatePayload = {
      name: name.trim(),
      memory_mb: memory,
      vcpus,
      disk_size_gb: source === 'import' ? 0 : diskSize,
      disk_format: diskFormat,
      os_variant: osVariant || undefined,
      bridge,
      pool_path: poolPath,
      firmware,
      autostart,
    };
    if (source === 'iso') payload.iso_path = isoPath;
    if (source === 'import') payload.import_disk = importDisk.trim();

    try {
      const res = await create.mutateAsync(payload);
      if (res && res.success === false) {
        setErr(res.error || 'Creation failed.');
        return;
      }
      close();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const loadingInfo = info.isLoading && Boolean(hostID);

  return (
    <Modal
      open={open}
      onClose={close}
      title="Create Virtual Machine"
      width={620}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={close} disabled={create.isPending}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={create.isPending || !hostID}>
            {create.isPending ? 'Creating VM…' : 'Create VM'}
          </button>
        </>
      }
    >
      <Field label="Host">
        <Select value={hostID} onChange={(e) => setHostID(e.target.value)}>
          {onlineHosts.length === 0 ? <option value="">No online hosts available</option> : null}
          {onlineHosts.map((h) => (
            <option key={h.id} value={h.id}>
              {h.id} ({h.ip}) — {(h.mem_free_mb / 1024).toFixed(1)} GB free
            </option>
          ))}
        </Select>
      </Field>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px 100px', gap: 10 }}>
        <Field label="Name">
          <TextInput
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ubuntu-web-01"
          />
        </Field>
        <Field label="vCPUs">
          <TextInput
            type="number"
            min={1}
            max={128}
            value={vcpus}
            onChange={(e) => setVCPUs(parseInt(e.target.value) || 1)}
          />
        </Field>
        <Field label="Memory (MB)">
          <TextInput
            type="number"
            min={128}
            step={128}
            value={memory}
            onChange={(e) => setMemory(parseInt(e.target.value) || 128)}
          />
        </Field>
      </div>

      <Field label="Install source">
        <div style={radioGroup}>
          <RadioPill label="From ISO" active={source === 'iso'} onClick={() => setSource('iso')} />
          <RadioPill label="Import existing disk" active={source === 'import'} onClick={() => setSource('import')} />
          <RadioPill label="PXE boot" active={source === 'pxe'} onClick={() => setSource('pxe')} />
        </div>
      </Field>

      {source === 'iso' && (
        <Field label="ISO image" hint={loadingInfo ? 'Loading options…' : `${info.data?.isos.length ?? 0} found on host`}>
          {(info.data?.isos ?? []).length === 0 ? (
            <TextInput
              value={isoPath}
              onChange={(e) => setISOPath(e.target.value)}
              placeholder="/var/lib/libvirt/images/foo.iso"
            />
          ) : (
            <Select value={isoPath} onChange={(e) => setISOPath(e.target.value)}>
              {info.data!.isos.map((iso) => (
                <option key={iso} value={iso}>{iso}</option>
              ))}
            </Select>
          )}
        </Field>
      )}
      {source === 'import' && (
        <Field label="Existing disk image path" hint="Full path to an existing qcow2 / raw / vmdk disk image on this host">
          <TextInput
            value={importDisk}
            onChange={(e) => setImportDisk(e.target.value)}
            placeholder="/var/lib/libvirt/images/existing.qcow2"
            style={{ fontFamily: "'SF Mono', monospace" }}
          />
        </Field>
      )}

      {source !== 'import' && (
        <div style={{ display: 'grid', gridTemplateColumns: '140px 1fr', gap: 10 }}>
          <Field label="Disk size (GB)">
            <TextInput
              type="number"
              min={1}
              value={diskSize}
              onChange={(e) => setDiskSize(parseInt(e.target.value) || 1)}
            />
          </Field>
          <Field label="Disk format">
            <Select
              value={diskFormat}
              onChange={(e) => setDiskFormat(e.target.value as 'qcow2' | 'raw')}
            >
              <option value="qcow2">qcow2</option>
              <option value="raw">raw</option>
            </Select>
          </Field>
        </div>
      )}

      <Field label="Network bridge" hint={loadingInfo ? 'Loading bridges…' : `${info.data?.bridges.length ?? 0} bridges on host`}>
        {(info.data?.bridges ?? []).length === 0 ? (
          <TextInput value={bridge} onChange={(e) => setBridge(e.target.value)} placeholder="br0" />
        ) : (
          <Select value={bridge} onChange={(e) => setBridge(e.target.value)}>
            {info.data!.bridges.map((b) => (
              <option key={b} value={b}>{b}</option>
            ))}
          </Select>
        )}
      </Field>

      <Field label="Storage pool" hint={loadingInfo ? 'Loading pools…' : `${info.data?.pools.length ?? 0} pools on host`}>
        {(info.data?.pools ?? []).length === 0 ? (
          <TextInput value={poolPath} onChange={(e) => setPoolPath(e.target.value)} />
        ) : (
          <Select value={poolPath} onChange={(e) => setPoolPath(e.target.value)}>
            {info.data!.pools.map((p) => (
              <option key={p.name} value={p.path}>{p.name} — {p.path}</option>
            ))}
          </Select>
        )}
      </Field>

      <details style={detailsStyle}>
        <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--text-muted)' }}>
          Advanced
        </summary>

        <Field label="OS variant" hint="Optional — tells virt-install which OS optimisations to enable">
          <Select value={osVariant} onChange={(e) => setOsVariant(e.target.value)}>
            <option value="">— auto / skip —</option>
            {(info.data?.os_variants ?? []).slice(0, 400).map((v) => (
              <option key={v} value={v}>{v}</option>
            ))}
          </Select>
        </Field>

        <Field label="Firmware">
          <Select value={firmware} onChange={(e) => setFirmware(e.target.value as 'bios' | 'efi')}>
            <option value="bios">BIOS (SeaBIOS)</option>
            <option value="efi">UEFI (OVMF)</option>
          </Select>
        </Field>

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10, cursor: 'pointer' }}>
          <input type="checkbox" checked={autostart} onChange={(e) => setAutostart(e.target.checked)} />
          <span style={{ fontSize: 12, color: 'var(--text-body)' }}>
            Start automatically when the hypervisor boots
          </span>
        </label>
      </details>

      <ErrorBanner message={err} />
    </Modal>
  );
}

function RadioPill({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <div
      role="radio"
      aria-checked={active}
      onClick={onClick}
      style={{
        padding: '6px 14px',
        borderRadius: 4,
        fontSize: 12,
        cursor: 'pointer',
        border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
        background: active ? 'var(--accent-subtle)' : 'var(--bg-input)',
        color: active ? 'var(--accent-hover)' : 'var(--text-body)',
      }}
    >
      {label}
    </div>
  );
}

const radioGroup: React.CSSProperties = {
  display: 'flex',
  gap: 8,
  flexWrap: 'wrap',
};
const detailsStyle: React.CSSProperties = {
  marginTop: 14,
  padding: '10px 14px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
};
