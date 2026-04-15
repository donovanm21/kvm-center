import type { VM } from '@/types';

/**
 * Single source of truth for the kvm-tools status pill + classification.
 * Used by the Tools update centre, the VMs list, and the Host detail
 * VMs tab so the three surfaces always tell the same story.
 */

export type KVMToolsStatus = 'current' | 'outdated' | 'missing' | 'no-tools';

export const KVM_TOOLS_STATUS_ORDER: Record<KVMToolsStatus, number> = {
  outdated: 0,
  missing: 1,
  'no-tools': 2,
  current: 3,
};

export function classifyKVMTools(v: VM, latest: string): KVMToolsStatus {
  if (!v.guest_agent) return 'no-tools';
  if (!v.kvm_tools_version) return 'missing';
  if (!latest || v.kvm_tools_version === latest) return 'current';
  return 'outdated';
}

const TONE: Record<KVMToolsStatus, { tone: 'ok' | 'warn' | 'danger' | 'muted'; label: string }> = {
  current:    { tone: 'ok',     label: 'current' },
  outdated:   { tone: 'warn',   label: 'outdated' },
  missing:    { tone: 'danger', label: 'missing' },
  'no-tools': { tone: 'muted',  label: 'no tools' },
};

interface KVMToolsPillProps {
  vm: VM;
  latest: string;
  /** Show "QUEUED" tone-warn pill instead of the classified state. */
  queued?: boolean;
  /** Render the reported version next to the pill (e.g. "current · v5.17.0"). */
  showVersion?: boolean;
}

/**
 * Pill styled per the Style Guide tone tokens, pairing the state with
 * the reported kvm-tools version when there is one. Tooltip carries
 * the long-form explanation so the tight pill stays glanceable.
 */
export function KVMToolsPill({ vm, latest, queued, showVersion }: KVMToolsPillProps) {
  if (queued) {
    return <span className="tone-pill warn" title="Self-update queued — will trigger on the next telemetry tick">queued</span>;
  }
  const status = classifyKVMTools(vm, latest);
  const tone = TONE[status];
  const reported = vm.kvm_tools_version;

  let label = tone.label;
  if (showVersion && status !== 'no-tools') {
    if (status === 'current' && reported) label = `current · ${reported}`;
    else if (status === 'outdated' && reported) label = `outdated · ${reported}`;
    else if (status === 'missing') label = 'missing';
  }

  return (
    <span className={`tone-pill ${tone.tone}`} title={describe(status, reported, latest, vm.state)}>
      {label}
    </span>
  );
}

function describe(status: KVMToolsStatus, reported: string | undefined, latest: string, state: string): string {
  switch (status) {
    case 'current':
      return `Running ${latest}`;
    case 'outdated':
      return `Reporting ${reported ?? '?'} — server ships ${latest}. Queue an update from the Tools tab.`;
    case 'missing':
      return state === 'running'
        ? 'Guest agent live but kvm-tools version not reported — older daemon or qemu-ga fallback.'
        : 'VM is not running — start it, then install kvm-tools.';
    case 'no-tools':
      return state === 'running'
        ? 'No guest agent responding — install kvm-tools to surface OS / IP / filesystem data.'
        : 'VM is not running — no telemetry available.';
  }
}
