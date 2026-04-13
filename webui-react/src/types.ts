// Mirror of internal/models for the slice of fields the UI cares about.
// When the backend stabilises we'll generate this from the Go source
// (e.g. via a tygo / openapi step) — for Phase 1 hand-maintained is fine
// and keeps the surface small.

export type HostStatus = 'online' | 'offline';
export type VMState = 'running' | 'stopped' | 'paused' | 'crashed' | 'unknown';

export interface Host {
  id: string;
  ip: string;
  agent_port: number;
  cpu_usage: number;
  cpu_count: number;
  mem_usage: number;
  mem_total_mb: number;
  mem_free_mb: number;
  vm_count: number;
  status: HostStatus;
  last_seen: string;
  os?: string;
  kernel_ver?: string;
  libvirt_ver?: string;
  uptime?: string;
  cpu_model?: string;
}

export interface VMDisk {
  target: string;
  device: string;
  disk_type: string;
  bus: string;
  path: string;
  capacity_gb: number;
  alloc_gb: number;
}

export interface VMPCIDevice {
  source_addr: string;
  host_desc: string;
}

export interface VMNetwork {
  interface: string;
  mac: string;
  bridge: string;
  rx_bytes: number;
  tx_bytes: number;
}

export interface GuestFS {
  mountpoint: string;
  fstype: string;
  total_gb: number;
  used_gb: number;
}

export interface VM {
  uuid: string;
  name: string;
  host_id: string;
  state: VMState;
  cpu_count: number;
  cpu_usage: number;
  mem_total_mb: number;
  mem_usage_mb: number;
  disk_usage_gb: number;
  disk_total_gb: number;
  snapshot_count: number;
  guest_agent: boolean;
  guest_source?: string;
  guest_os?: string;
  guest_hostname?: string;
  guest_kernel?: string;
  guest_uptime?: string;
  guest_ips?: string[];
  guest_fs?: GuestFS[];
  disk_type?: string;
  firmware?: string;
  machine_type?: string;
  cpu_mode?: string;
  autostart?: boolean;
  has_tpm?: boolean;
  tpm_version?: string;
  video_model?: string;
  boot_dev?: string;
  all_disks?: VMDisk[];
  pci_devices?: VMPCIDevice[];
  networks?: VMNetwork[];
  notes?: string;
}

export interface Snapshot {
  name: string;
  domain: string;
  creation_time: string;
  state: string;
  parent?: string;
  is_current: boolean;
}

export interface StoragePool {
  name: string;
  host_id: string;
  path: string;
  capacity_gb: number;
  allocation_gb: number;
  available_gb: number;
  state: string;
  pool_type: string;
}

export interface CurrentUser {
  id: string;
  username: string;
  email?: string;
  enabled: boolean;
  role_ids?: string[];
  group_ids?: string[];
}

export interface MeResponse {
  user: CurrentUser;
  permissions: string[];
}

export interface Features {
  disable_vm_create?: boolean;
  disable_vm_delete?: boolean;
  disable_snapshots?: boolean;
  disable_console?: boolean;
  disable_migrations?: boolean;
  disable_storage_upload?: boolean;
  disable_storage_delete?: boolean;
}
