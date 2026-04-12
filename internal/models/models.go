package models

import "time"

// HostStatus represents the connectivity state of a hypervisor.
type HostStatus string

const (
	HostOnline  HostStatus = "online"
	HostOffline HostStatus = "offline"
)

// VMState maps libvirt domain states to human-readable strings.
type VMState string

const (
	VMRunning VMState = "running"
	VMStopped VMState = "stopped"
	VMPaused  VMState = "paused"
	VMCrashed VMState = "crashed"
	VMUnknown VMState = "unknown"
)

// Host represents a KVM hypervisor node.
type Host struct {
	ID         string     `json:"id"`
	IP         string     `json:"ip"`
	AgentPort  int        `json:"agent_port"`
	CPUUsage   float64    `json:"cpu_usage"`
	CPUCount   int        `json:"cpu_count"`
	MemUsage   float64    `json:"mem_usage"`
	MemTotalMB uint64     `json:"mem_total_mb"`
	MemFreeMB  uint64     `json:"mem_free_mb"`
	VMCount    int        `json:"vm_count"`
	Status     HostStatus `json:"status"`
	LastSeen   time.Time  `json:"last_seen"`
	OS         string     `json:"os"`
	KernelVer  string     `json:"kernel_ver"`
	LibvirtVer string     `json:"libvirt_ver"`
	Uptime     string     `json:"uptime"`

	// Hardware details collected from virsh nodeinfo, lspci, /sys/kernel/iommu_groups/.
	CPUModel       string          `json:"cpu_model,omitempty"`
	Sockets        int             `json:"sockets,omitempty"`
	CoresPerSocket int             `json:"cores_per_socket,omitempty"`
	ThreadsPerCore int             `json:"threads_per_core,omitempty"`
	NUMANodes      int             `json:"numa_nodes,omitempty"`
	PCIDevices     []HostPCIDevice `json:"host_pci_devices,omitempty"`
	GPUs           []string        `json:"gpus,omitempty"`
}

// HostPCIDevice is one PCI device on the hypervisor (from lspci -nn).
type HostPCIDevice struct {
	Address     string `json:"address"`
	Type        string `json:"type"`
	Description string `json:"description"`
	IOMMUGroup  int    `json:"iommu_group"`
	InUseByVM   string `json:"in_use_by_vm,omitempty"`
}

// VMNetwork represents a single virtual NIC attached to a VM.
type VMNetwork struct {
	Interface string `json:"interface"`
	MAC       string `json:"mac"`
	Bridge    string `json:"bridge"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
}

// VM represents a virtual machine managed by libvirt.
type VM struct {
	UUID          string      `json:"uuid"`
	Name          string      `json:"name"`
	HostID        string      `json:"host_id"`
	State         VMState     `json:"state"`
	CPUCount      int         `json:"cpu_count"`
	CPUUsage      float64     `json:"cpu_usage"`
	MemTotalMB    uint64      `json:"mem_total_mb"`
	MemUsageMB    uint64      `json:"mem_usage_mb"`
	DiskPath      string      `json:"disk_path"`
	DiskUsageGB   float64     `json:"disk_usage_gb"`
	DiskTotalGB   float64     `json:"disk_total_gb"`
	Networks      []VMNetwork `json:"networks"`
	SnapshotCount int         `json:"snapshot_count"`

	// Hardware config from virsh dumpxml + dominfo.
	DiskType    string           `json:"disk_type,omitempty"`    // "qcow2", "raw"
	Firmware    string           `json:"firmware,omitempty"`     // "bios", "efi", "efi+secureboot"
	MachineType string           `json:"machine_type,omitempty"` // "pc-q35-rhel9.6.0"
	CPUMode     string           `json:"cpu_mode,omitempty"`     // "host-passthrough", "custom"
	Autostart   bool             `json:"autostart"`
	HasTPM      bool             `json:"has_tpm"`
	TPMVersion  string           `json:"tpm_version,omitempty"`
	VideoModel  string           `json:"video_model,omitempty"`
	BootDev     string           `json:"boot_dev,omitempty"`
	AllDisks    []VMDisk         `json:"all_disks,omitempty"`
	PCIDevices  []VMPCIDevice    `json:"pci_devices,omitempty"`

	// Guest-side fields. Populated by qemu-guest-agent (via hypervisor) or
	// kvm-tools daemon (via POST /api/guest-telemetry). Empty when no agent.
	GuestAgent    bool      `json:"guest_agent"`            // true if any guest source responded
	GuestSource   string    `json:"guest_source,omitempty"` // "qemu-ga" | "kvm-tools"
	GuestOS       string    `json:"guest_os,omitempty"`
	GuestHostname string    `json:"guest_hostname,omitempty"`
	GuestKernel   string    `json:"guest_kernel,omitempty"`
	GuestUptime   string    `json:"guest_uptime,omitempty"`
	GuestIPs      []string  `json:"guest_ips,omitempty"`
	GuestFS       []GuestFS `json:"guest_fs,omitempty"`

	// User-editable, persisted in server store (rehydrated by UUID).
	Notes string `json:"notes,omitempty"`
}

// GuestFS is one filesystem mount reported by a guest agent.
type GuestFS struct {
	Mountpoint string  `json:"mountpoint"`
	FSType     string  `json:"fstype"`
	TotalGB    float64 `json:"total_gb"`
	UsedGB     float64 `json:"used_gb"`
}

// VMDisk is one disk attached to a VM (from virsh dumpxml).
type VMDisk struct {
	Target     string  `json:"target"`      // "vda", "sda"
	Device     string  `json:"device"`      // "disk", "cdrom"
	DiskType   string  `json:"disk_type"`   // "qcow2", "raw"
	Bus        string  `json:"bus"`         // "virtio", "scsi", "sata"
	Path       string  `json:"path"`
	CapacityGB float64 `json:"capacity_gb"`
	AllocGB    float64 `json:"alloc_gb"`
}

// VMPCIDevice is one PCI device passed through to a VM via VFIO.
type VMPCIDevice struct {
	SourceAddr string `json:"source_addr"` // "0000:05:00.0"
	HostDesc   string `json:"host_desc"`   // resolved lspci description
}

// GuestTelemetryPayload is sent by the kvm-tools daemon running inside a VM
// directly to POST /api/guest-telemetry on the kvm-center server.
type GuestTelemetryPayload struct {
	VMUUID     string    `json:"vm_uuid"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Kernel     string    `json:"kernel"`
	Uptime     string    `json:"uptime"`
	CPUCount   int       `json:"cpu_count"`
	MemTotalMB uint64    `json:"mem_total_mb"`
	MemUsedMB  uint64    `json:"mem_used_mb"`
	IPs        []string  `json:"ips"`
	FS         []GuestFS `json:"fs"`
	Timestamp  time.Time `json:"timestamp"`
}

// Snapshot represents a libvirt domain snapshot.
type Snapshot struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	CreationTime string `json:"creation_time"`
	State        string `json:"state"`
	Parent       string `json:"parent,omitempty"`
	IsCurrent    bool   `json:"is_current"`
}

// StoragePool represents a libvirt storage pool on a host.
type StoragePool struct {
	Name        string  `json:"name"`
	HostID      string  `json:"host_id"`
	Path        string  `json:"path"`
	CapacityGB  float64 `json:"capacity_gb"`
	AllocationGB float64 `json:"allocation_gb"`
	AvailableGB float64 `json:"available_gb"`
	State       string  `json:"state"`
	PoolType    string  `json:"pool_type"`
}

// TelemetryPayload is the JSON body sent by an agent to POST /api/telemetry.
type TelemetryPayload struct {
	HostID     string        `json:"host_id"`
	IP         string        `json:"ip"`
	AgentPort  int           `json:"agent_port"`
	OS         string        `json:"os"`
	KernelVer  string        `json:"kernel_ver"`
	LibvirtVer string        `json:"libvirt_ver"`
	CPUUsage   float64       `json:"cpu_usage"`
	CPUCount   int           `json:"cpu_count"`
	MemUsage   float64       `json:"mem_usage"`
	MemTotalMB uint64        `json:"mem_total_mb"`
	MemFreeMB  uint64        `json:"mem_free_mb"`
	Uptime     string        `json:"uptime"`
	VMs        []VM          `json:"vms"`
	Storage    []StoragePool `json:"storage"`
	Timestamp  time.Time     `json:"timestamp"`

	// Host hardware (collected once or on change).
	CPUModel       string          `json:"cpu_model,omitempty"`
	Sockets        int             `json:"sockets,omitempty"`
	CoresPerSocket int             `json:"cores_per_socket,omitempty"`
	ThreadsPerCore int             `json:"threads_per_core,omitempty"`
	NUMANodes      int             `json:"numa_nodes,omitempty"`
	PCIDevices     []HostPCIDevice `json:"host_pci_devices,omitempty"`
	GPUs           []string        `json:"gpus,omitempty"`
}

// MigrationRecord is one completed or failed migration, persisted in the server store.
type MigrationRecord struct {
	ID          string `json:"id"`
	VMName      string `json:"vm_name"`
	SourceHost  string `json:"source_host"`
	DestHost    string `json:"dest_host"`
	Mode        string `json:"mode"`        // "offline" | "live"
	Status      string `json:"status"`      // "running" | "complete" | "failed"
	StartedAt   int64  `json:"started_at"`  // unix seconds
	CompletedAt int64  `json:"completed_at,omitempty"`
	ElapsedSec  int64  `json:"elapsed_sec,omitempty"`
	DiskSizeMB  int64  `json:"disk_size_mb,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Error       string `json:"error,omitempty"`
	Progress    int    `json:"progress"`
	Phase       string `json:"phase,omitempty"`
}

// CommandRequest is sent by the server to an agent's POST /command endpoint.
type CommandRequest struct {
	Action string            `json:"action"` // START, STOP, RESTART, SNAPSHOT, MIGRATE
	VMName string            `json:"vm_name"`
	VMUUID string            `json:"vm_uuid"`
	Params map[string]string `json:"params,omitempty"`
}

// CommandResponse is returned by the agent after executing a command.
type CommandResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// APICommandRequest is sent by the web UI to POST /api/command on the server.
type APICommandRequest struct {
	HostID string            `json:"host_id"`
	VMName string            `json:"vm_name"`
	VMUUID string            `json:"vm_uuid"`
	Action string            `json:"action"`
	Params map[string]string `json:"params,omitempty"`
}
