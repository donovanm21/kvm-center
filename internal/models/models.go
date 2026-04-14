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

	// Maintenance mode (v5.2). When true, the server refuses new-VM
	// placements and migration targets that land on this host, while
	// still allowing migrations OFF the host (the whole point — drain
	// before patching). Operators flip the flag via POST
	// /api/hosts/{id}/maintenance/enter|exit.
	MaintenanceMode      bool      `json:"maintenance_mode,omitempty"`
	MaintenanceEnteredAt time.Time `json:"maintenance_entered_at,omitempty"`
	MaintenanceBy        string    `json:"maintenance_by,omitempty"`   // actor user ID
	MaintenanceReason    string    `json:"maintenance_reason,omitempty"`
}

// AuditEvent (v5.2) records a single mutating operation. Every handler
// that changes persisted state calls audit(...) — see server/audit.go
// — to produce one of these. Surfaced read-only in Settings → Audit
// Log; operators filter by actor / action / time range / outcome.
type AuditEvent struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	ActorID   string            `json:"actor_id,omitempty"`   // user ID; empty for unauthenticated (e.g. pre-session)
	ActorName string            `json:"actor_name,omitempty"` // denormalised for UI without a join
	ActorIP   string            `json:"actor_ip,omitempty"`
	Action    string            `json:"action"`   // "vm.start", "role.update", "tls.upload", etc.
	Resource  string            `json:"resource,omitempty"` // "vm/<uuid>", "host/<id>", "role/<id>", free-form
	Detail    map[string]string `json:"detail,omitempty"`   // context (from→to values, counts, etc.)
	Success   bool              `json:"success"`
	Error     string            `json:"error,omitempty"`
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
	GuestIPs            []string         `json:"guest_ips,omitempty"`
	GuestFS             []GuestFS        `json:"guest_fs,omitempty"`
	GuestInterfaces     []GuestInterface `json:"guest_interfaces,omitempty"`
	KVMToolsVersion     string           `json:"kvm_tools_version,omitempty"`
	KVMToolsUpdateQueued bool            `json:"kvm_tools_update_queued,omitempty"`

	// User-editable, persisted in server store (rehydrated by UUID).
	Notes string `json:"notes,omitempty"`
}

// GuestInterface is one network interface reported by the in-guest
// kvm-tools daemon, classified so the UI can flag Docker bridges,
// VPN tunnels, and container veth pairs separately from the primary
// NIC. Loopback is filtered out at collection time.
type GuestInterface struct {
	Name  string   `json:"name"`            // eth0, docker0, br-1a2b…, tailscale0
	MAC   string   `json:"mac,omitempty"`
	Addrs []string `json:"addrs,omitempty"` // IPv4 + IPv6 with CIDR
	// Kind buckets the interface so the UI can colour-code:
	//   "primary"   — first non-loopback, non-virtual interface
	//   "docker"    — docker0 / Docker custom bridge (br-<hex>)
	//   "container" — veth pair, podman, cni*, flannel*, weave, calico*
	//   "vpn"       — wg*, tailscale*, tun*, tap*, zt*, ppp*
	//   "bridge"    — generic bridge (virbr*, br0/br1 …)
	//   "other"     — anything else not loopback
	Kind string `json:"kind"`
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
	IPs             []string         `json:"ips"`
	Interfaces      []GuestInterface `json:"interfaces,omitempty"`
	FS              []GuestFS        `json:"fs"`
	KVMToolsVersion string           `json:"kvm_tools_version,omitempty"`
	Timestamp       time.Time        `json:"timestamp"`
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

// ─── Authentication / RBAC ──────────────────────────────────────────────────

// User is a local account that can log in to the web UI.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	PasswordHash string    `json:"password_hash,omitempty"` // omitted from API responses
	Enabled      bool      `json:"enabled"`
	RoleIDs      []string  `json:"role_ids"`  // direct role assignments
	GroupIDs     []string  `json:"group_ids"` // group memberships (inherits group roles)
	CreatedAt    time.Time `json:"created_at"`
	LastLoginAt  time.Time `json:"last_login_at,omitempty"`

	// Optional TOTP (RFC 6238) second factor. TOTPSecret is a base32-encoded
	// 20-byte secret; it is populated only after the user completes enrollment
	// (scans the code + verifies with a live TOTP). TOTPEnabled gates whether
	// the login flow demands the second factor.
	TOTPSecret  string `json:"totp_secret,omitempty"`  // omitted from API responses via sanitizeUser
	TOTPEnabled bool   `json:"totp_enabled"`

	// External identities linked to this account (OIDC / OAuth2 / SAML / LDAP).
	// Each entry is the (provider_id, subject) pair returned by the IdP and
	// lets the same local account sign in via multiple sources.
	ExternalIDs []ExternalIdentity `json:"external_ids,omitempty"`
}

// ExternalIdentity is a single federated-login binding.
type ExternalIdentity struct {
	ProviderID string    `json:"provider_id"`
	Subject    string    `json:"subject"`
	Email      string    `json:"email,omitempty"`
	LinkedAt   time.Time `json:"linked_at"`
}

// IdentityProvider configures one external authentication source. Kind
// hints at the UI preset (Google / GitHub / Microsoft / OIDC) but all
// federated login flows currently share the OAuth2 + userinfo code path.
type IdentityProvider struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`           // "google" | "github" | "microsoft" | "oidc"
	Name          string    `json:"name"`           // display label on the login button
	Enabled       bool      `json:"enabled"`
	Issuer        string    `json:"issuer,omitempty"` // if set, discovery fills the URLs below
	AuthURL       string    `json:"auth_url,omitempty"`
	TokenURL      string    `json:"token_url,omitempty"`
	UserinfoURL   string    `json:"userinfo_url,omitempty"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret,omitempty"` // omitted from API responses
	Scopes        string    `json:"scopes,omitempty"`        // space-separated, e.g. "openid email profile"
	DefaultRoleID string    `json:"default_role_id,omitempty"`
	SubjectClaim  string    `json:"subject_claim,omitempty"`  // default "sub"; GitHub uses "id"
	EmailClaim    string    `json:"email_claim,omitempty"`    // default "email"
	UsernameClaim string    `json:"username_claim,omitempty"` // default "preferred_username" then "email"

	// LDAP-specific config, only consulted when Kind == "ldap".
	LDAP *LDAPConfig `json:"ldap,omitempty"`

	// SAML-specific config, only consulted when Kind == "saml".
	SAML *SAMLConfig `json:"saml,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// LDAPConfig is everything needed to bind against an LDAP/AD directory
// with a username+password pair and read the user's attributes. It lives
// under IdentityProvider so LDAP shares the same enable/disable toggle,
// default-role mapping, and CRUD path as OIDC providers.
type LDAPConfig struct {
	URL              string `json:"url"`                // ldap://host:389 or ldaps://host:636
	BindDN           string `json:"bind_dn,omitempty"`  // service account used for user lookup; blank → bind directly as user
	BindPassword     string `json:"bind_password,omitempty"` // omitted from API responses
	UserBaseDN       string `json:"user_base_dn"`       // DN under which users live
	UserFilter       string `json:"user_filter"`        // e.g. "(sAMAccountName=%s)" — %s is the submitted username
	UsernameAttr     string `json:"username_attr,omitempty"` // default: sAMAccountName (AD) / uid (OpenLDAP)
	EmailAttr        string `json:"email_attr,omitempty"`    // default: mail
	StartTLS         bool   `json:"start_tls,omitempty"`
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
}

// SAMLConfig holds the minimum required to drive an SP-initiated SAML 2.0
// sign-in flow against an external IdP. Either MetadataURL alone (we'll
// fetch + parse on demand) or the manual trio (IDPEntityID, SSOURL,
// Certificate) must be populated.
type SAMLConfig struct {
	MetadataURL  string `json:"metadata_url,omitempty"`
	IDPEntityID  string `json:"idp_entity_id,omitempty"`
	SSOURL       string `json:"sso_url,omitempty"`
	Certificate  string `json:"certificate,omitempty"` // PEM-encoded IdP signing certificate

	// SP (our) side. When blank we synthesize entity_id = <base_url>/api/auth/saml/metadata?provider=<id>
	SPEntityID   string `json:"sp_entity_id,omitempty"`

	// Attribute mapping — leave blank for sensible defaults (NameID for
	// subject, "email"/"mail" for email, "preferred_username" or the
	// email local-part for username).
	SubjectAttr  string `json:"subject_attr,omitempty"`
	EmailAttr    string `json:"email_attr,omitempty"`
	UsernameAttr string `json:"username_attr,omitempty"`
}

// Group is a collection of users that share a common set of role assignments.
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	RoleIDs     []string  `json:"role_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

// Role is a named bundle of permission strings. Built-in roles cannot be
// deleted or have their permissions modified, but can be assigned freely.
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"` // permission keys from the Perm* constants
	BuiltIn     bool      `json:"built_in"`
	CreatedAt   time.Time `json:"created_at"`
}

// Session is an authenticated cookie-backed session.
type Session struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UserAgent string    `json:"user_agent,omitempty"`
}

// Features is the global feature-flag set editable by an administrator
// from Settings → General. A zero value means "everything enabled", so
// existing deployments upgrade cleanly. Each flag is phrased as a
// *disable* so you can't accidentally turn the whole product off by
// forgetting a field.
type Features struct {
	DisableVMCreate      bool `json:"disable_vm_create"`
	DisableVMDelete      bool `json:"disable_vm_delete"`
	DisableSnapshots     bool `json:"disable_snapshots"`
	DisableConsole       bool `json:"disable_console"`
	DisableMigrations    bool `json:"disable_migrations"`
	DisableStorageUpload bool `json:"disable_storage_upload"`
	DisableStorageDelete bool `json:"disable_storage_delete"`
}

// Permission constants. PermAdmin is a wildcard granting everything.
const (
	PermAdmin         = "admin"
	PermVMView        = "vm.view"
	PermVMLifecycle   = "vm.lifecycle" // start/stop/restart/force-off
	PermVMCreate      = "vm.create"
	PermVMDelete      = "vm.delete"
	PermVMMigrate     = "vm.migrate"
	PermVMSnapshot    = "vm.snapshot"
	PermVMConsole     = "vm.console"
	PermVMEdit        = "vm.edit" // notes + hardware
	PermHostView      = "host.view"
	PermHostManage    = "host.manage"
	PermStorageView   = "storage.view"
	PermStorageUpload = "storage.upload"
	PermStorageDelete = "storage.delete"
	PermUserManage    = "user.manage" // create/update/disable users, groups, roles
	PermTLSManage     = "tls.manage"  // upload / regenerate / download server TLS cert
	PermAuditView     = "audit.view"  // read the audit log
)

// AllPermissions is the full list used for UI dropdowns and role editing.
var AllPermissions = []string{
	PermAdmin,
	PermVMView, PermVMLifecycle, PermVMCreate, PermVMDelete,
	PermVMMigrate, PermVMSnapshot, PermVMConsole, PermVMEdit,
	PermHostView, PermHostManage,
	PermStorageView, PermStorageUpload, PermStorageDelete,
	PermUserManage, PermTLSManage, PermAuditView,
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
