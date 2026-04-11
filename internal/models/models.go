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
	UUID      string      `json:"uuid"`
	Name      string      `json:"name"`
	HostID    string      `json:"host_id"`
	State     VMState     `json:"state"`
	CPUCount  int         `json:"cpu_count"`
	CPUUsage  float64     `json:"cpu_usage"`
	MemTotalMB uint64     `json:"mem_total_mb"`
	MemUsageMB uint64     `json:"mem_usage_mb"`
	DiskPath  string      `json:"disk_path"`
	DiskUsageGB float64   `json:"disk_usage_gb"`
	DiskTotalGB float64   `json:"disk_total_gb"`
	Networks  []VMNetwork `json:"networks"`
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
