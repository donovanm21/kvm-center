// Mirror of internal/models for the slice of fields the UI cares about.
// When the backend stabilises we'll generate this from the Go source
// (e.g. via a tygo / openapi step) — for Phase 1 hand-maintained is fine
// and keeps the surface small.

export type HostStatus = 'online' | 'offline';
export type VMState = 'running' | 'stopped' | 'paused' | 'crashed' | 'unknown';

export interface HostPCIDevice {
  address: string;
  type: string;
  description: string;
  iommu_group: number;
  in_use_by_vm?: string;
}

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
  sockets?: number;
  cores_per_socket?: number;
  threads_per_core?: number;
  numa_nodes?: number;
  host_pci_devices?: HostPCIDevice[];
  gpus?: string[];
  maintenance_mode?: boolean;
  maintenance_entered_at?: string;
  maintenance_by?: string;
  maintenance_reason?: string;
  mtls_ready?: boolean;
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

export type GuestInterfaceKind = 'primary' | 'docker' | 'container' | 'vpn' | 'bridge' | 'other';

export interface MaintenanceMeta {
  maintenance_mode?: boolean;
  maintenance_entered_at?: string;
  maintenance_by?: string;
  maintenance_reason?: string;
  mtls_ready?: boolean;
}
export interface GuestInterface {
  name: string;
  mac?: string;
  addrs?: string[];
  kind: GuestInterfaceKind;
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
  guest_interfaces?: GuestInterface[];
  kvm_tools_version?: string;
  kvm_tools_update_queued?: boolean;
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

export interface StorageEntry {
  name: string;
  is_dir: boolean;
  size: number;
  modified?: string;
}

export interface StorageBrowseResponse {
  host_id: string;
  path: string;
  entries: StorageEntry[];
}

export interface MigrationRecord {
  id: string;
  vm_name: string;
  source_host: string;
  dest_host: string;
  mode: string;
  transport?: 'tls' | 'ssh' | string;
  status: 'running' | 'complete' | 'failed' | string;
  started_at: number;
  completed_at?: number;
  elapsed_sec?: number;
  disk_size_mb?: number;
  detail?: string;
  error?: string;
  progress: number;
  phase?: string;
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
  totp_enabled?: boolean;
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
