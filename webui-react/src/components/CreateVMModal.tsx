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
  // v5.60.13 — sort by id so the picker is stable across refetches.
  // Operators complained about hosts "jumping around" when the
  // upstream /api/hosts response reordered between telemetry ticks.
  const onlineHosts = useMemo(
    () =>
      (hosts.data ?? [])
        .filter((h) => h.status === 'online')
        .slice()
        .sort((a, b) => a.id.localeCompare(b.id)),
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

  // v5.60.13 — agent's vm-create-info used to return nil slices for
  // empty lists (Go `var isos []string` → JSON `null`), which crashed
  // `.length` accessors throughout the modal. Coalesce defensively
  // alongside the agent-side fix; the SPA stays robust even against
  // pre-v5.60.13 agents.
  const infoIsos = useMemo(
    () => (Array.isArray(info.data?.isos) ? info.data!.isos : []),
    [info.data],
  );
  const infoBridges = useMemo(
    () => (Array.isArray(info.data?.bridges) ? info.data!.bridges : []),
    [info.data],
  );
  const infoPools = useMemo(
    () => (Array.isArray(info.data?.pools) ? info.data!.pools : []),
    [info.data],
  );
  const infoVariants = useMemo(
    () => (Array.isArray(info.data?.os_variants) ? info.data!.os_variants : []),
    [info.data],
  );

  // When info finishes loading, pre-fill bridge/pool/iso defaults with
  // whatever the agent reported so the form is usable on the first click.
  useEffect(() => {
    if (!info.data) return;
    if (!bridge && infoBridges.length > 0) setBridge(infoBridges[0]);
    if (poolPath === DEFAULT_POOL_PATH && infoPools.length > 0) {
      setPoolPath(infoPools[0].path);
    }
    if (!isoPath && source === 'iso' && infoIsos.length > 0) {
      setISOPath(infoIsos[0]);
    }
    // Intentionally exclude the setters from the dep list — we only want
    // this to run when fresh info arrives, not on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info.data, infoBridges, infoPools, infoIsos]);

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
        <HostPicker
          hosts={onlineHosts}
          value={hostID}
          onChange={setHostID}
        />
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
        <Field label="ISO image" hint={loadingInfo ? 'Loading options…' : `${infoIsos.length} found on host`}>
          {infoIsos.length === 0 ? (
            <TextInput
              value={isoPath}
              onChange={(e) => setISOPath(e.target.value)}
              placeholder="/var/lib/libvirt/images/foo.iso"
            />
          ) : (
            <Select value={isoPath} onChange={(e) => setISOPath(e.target.value)}>
              {infoIsos.map((iso) => (
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

      <Field label="Network bridge" hint={loadingInfo ? 'Loading bridges…' : `${infoBridges.length} bridges on host`}>
        {infoBridges.length === 0 ? (
          <TextInput value={bridge} onChange={(e) => setBridge(e.target.value)} placeholder="br0" />
        ) : (
          <Select value={bridge} onChange={(e) => setBridge(e.target.value)}>
            {infoBridges.map((b) => (
              <option key={b} value={b}>{b}</option>
            ))}
          </Select>
        )}
      </Field>

      <Field label="Storage pool" hint={loadingInfo ? 'Loading pools…' : `${infoPools.length} pools on host`}>
        {infoPools.length === 0 ? (
          <TextInput value={poolPath} onChange={(e) => setPoolPath(e.target.value)} />
        ) : (
          <Select value={poolPath} onChange={(e) => setPoolPath(e.target.value)}>
            {infoPools.map((p) => (
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
            {infoVariants.slice(0, 400).map((v) => (
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

/**
 * HostPicker — typeahead replacement for the prior `<Select>` host
 * dropdown. Two changes the operator asked for (v5.60.13):
 *
 *  - **Stable ordering.** Hosts are sorted alphabetically by id at
 *    the call site, so the picker doesn't reshuffle when telemetry
 *    refetches.
 *  - **Default 5 + search.** The dropdown shows the first 5 hosts
 *    alphabetically; the operator types to filter the rest. Keeps
 *    the picker usable at any fleet size without scrolling a giant
 *    native select.
 */
interface HostPickerHost {
  id: string;
  ip: string;
  mem_free_mb: number;
}
const HOST_PICKER_DEFAULT_VISIBLE = 5;
function HostPicker({
  hosts,
  value,
  onChange,
}: {
  hosts: HostPickerHost[];
  value: string;
  onChange: (id: string) => void;
}) {
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return hosts.slice(0, HOST_PICKER_DEFAULT_VISIBLE);
    // Substring match against id + ip so an operator can type "10.10"
    // or "node3" and find the host either way. Cap the rendered list
    // at 20 to keep the popover usable on a large fleet.
    return hosts
      .filter((h) => h.id.toLowerCase().includes(q) || h.ip.toLowerCase().includes(q))
      .slice(0, 20);
  }, [hosts, query]);

  const selected = hosts.find((h) => h.id === value) || null;
  const hiddenCount = !query.trim() && hosts.length > HOST_PICKER_DEFAULT_VISIBLE
    ? hosts.length - HOST_PICKER_DEFAULT_VISIBLE
    : 0;

  if (hosts.length === 0) {
    return (
      <TextInput value="" disabled placeholder="No online hosts available" />
    );
  }

  return (
    <div style={{ position: 'relative' }}>
      <TextInput
        value={open ? query : (selected ? `${selected.id} (${selected.ip}) — ${(selected.mem_free_mb / 1024).toFixed(1)} GB free` : '')}
        onFocus={() => { setOpen(true); setQuery(''); }}
        onBlur={() => {
          // Defer so an onMouseDown on the option below has a chance
          // to fire before the popover unmounts.
          setTimeout(() => setOpen(false), 120);
        }}
        onChange={(e) => setQuery(e.target.value)}
        placeholder={selected ? '' : `Search ${hosts.length} hosts…`}
      />
      {open && (
        <div
          style={{
            position: 'absolute',
            top: '100%',
            left: 0,
            right: 0,
            zIndex: 20,
            marginTop: 2,
            background: 'var(--bg-input)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            boxShadow: '0 6px 20px rgba(0,0,0,0.45)',
            maxHeight: 280,
            overflowY: 'auto',
          }}
        >
          {filtered.length === 0 ? (
            <div style={{ padding: '8px 10px', fontSize: 12, color: 'var(--text-faded)' }}>
              No host matches “{query}”.
            </div>
          ) : (
            filtered.map((h) => (
              <div
                key={h.id}
                onMouseDown={() => { onChange(h.id); setOpen(false); }}
                style={{
                  padding: '6px 10px',
                  cursor: 'pointer',
                  fontSize: 12,
                  background: h.id === value ? 'var(--accent-subtle)' : 'transparent',
                  color: h.id === value ? 'var(--accent-hover)' : 'var(--text-body)',
                  borderBottom: '1px solid var(--panel-border)',
                }}
                onMouseEnter={(e) => { if (h.id !== value) (e.currentTarget as HTMLDivElement).style.background = 'var(--bg-input-hover)'; }}
                onMouseLeave={(e) => { if (h.id !== value) (e.currentTarget as HTMLDivElement).style.background = 'transparent'; }}
              >
                <div style={{ fontFamily: "'SF Mono', monospace" }}>{h.id}</div>
                <div style={{ fontSize: 11, color: 'var(--text-faded)' }}>
                  {h.ip} · {(h.mem_free_mb / 1024).toFixed(1)} GB free
                </div>
              </div>
            ))
          )}
          {hiddenCount > 0 && (
            <div style={{ padding: '6px 10px', fontSize: 11, color: 'var(--text-faded)', borderTop: '1px solid var(--panel-border)' }}>
              {hiddenCount} more — type to search.
            </div>
          )}
        </div>
      )}
    </div>
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
