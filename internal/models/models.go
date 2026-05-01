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
	// AgentVersion (v5.33.0) is the hypervisor-agent binary version
	// reported in every telemetry tick. Used by the system-health
	// page to flag agents that haven't been refreshed after a
	// control-plane upgrade.
	AgentVersion string `json:"agent_version,omitempty"`

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
	MaintenanceBy        string    `json:"maintenance_by,omitempty"` // actor user ID
	MaintenanceReason    string    `json:"maintenance_reason,omitempty"`

	// MTLSReady (v5.10.1) is true when this host has an active
	// hypervisor cert issued by the cluster CA — a precondition for
	// qemu+tls:// migrations. Derived at read time from the
	// HypervisorCert store; operators can't set it manually.
	MTLSReady bool `json:"mtls_ready,omitempty"`
}

// SnapshotPolicy (v5.3) describes a recurring snapshot job: what to
// snapshot, how often, and how many to keep. The scheduler goroutine
// in snapshot_scheduler.go wakes once a minute, finds policies whose
// LastRunAt + EveryMinutes < now, issues a SNAPSHOT command through
// the usual agent plumbing, and prunes older policy-generated
// snapshots so RetentionKeep is honoured. Manual snapshots are left
// alone because cleanup only touches names that match the policy's
// snapshot-name prefix.
type SnapshotPolicy struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	VMUUIDs       []string  `json:"vm_uuids"`       // the VMs this policy covers
	EveryMinutes  int       `json:"every_minutes"`  // e.g. 60 hourly, 1440 daily, 10080 weekly
	RetentionKeep int       `json:"retention_keep"` // keep last N policy-generated snapshots
	CreatedAt     time.Time `json:"created_at"`
	LastRunAt     time.Time `json:"last_run_at,omitempty"`
	LastRunError  string    `json:"last_run_error,omitempty"`
	LastRunCount  int       `json:"last_run_count,omitempty"` // successful snapshots on last tick
}

// AuditShipping (v5.2.1) configures one external sink that receives
// every audit event as it's recorded. Keeps a compliance SIEM or log
// aggregator in sync without the operator scraping /api/audit.
// Disabled when Enabled=false — which is also the default so an
// upgraded server never starts shipping to a stale endpoint.
type AuditShipping struct {
	Enabled bool `json:"enabled"`

	// Transport: "syslog" or "http".
	Type string `json:"type,omitempty"`

	// Endpoint:
	//   syslog → "host:port" (UDP) or "tcp://host:port"
	//   http   → "https://aggregator.example.com/path"
	Endpoint string `json:"endpoint,omitempty"`

	// Optional HTTP auth. Bearer token ships as "Authorization: Bearer <v>".
	// Basic creds form "user:pass" and ship base64-encoded.
	BearerToken string `json:"bearer_token,omitempty"`
	BasicAuth   string `json:"basic_auth,omitempty"`

	// Syslog RFC 5424 facility/app-name. Default facility 1 (user-level),
	// app-name "kvm-center".
	SyslogFacility int    `json:"syslog_facility,omitempty"`
	SyslogAppName  string `json:"syslog_app_name,omitempty"`

	// Batching. Events queue up in memory until either BatchSize is
	// reached or FlushSeconds elapses, then are shipped together.
	BatchSize    int `json:"batch_size,omitempty"`    // default 50
	FlushSeconds int `json:"flush_seconds,omitempty"` // default 10

	// Operator-friendly observability: set by the worker after every
	// flush attempt so the UI can tell if shipping is actually working.
	LastShipAt    string `json:"last_ship_at,omitempty"`
	LastShipError string `json:"last_ship_error,omitempty"`
	LastShipCount int64  `json:"last_ship_count,omitempty"`
}

// NotificationChannel (v5.4) is one delivery target — webhook or
// email. Channels are referenced by ID from NotificationConfig.Events
// so the operator can route different event types to different sinks
// (e.g. a noisy "vm.action" stream into a chat webhook, but
// "host.offline" alerts into PagerDuty + email).
type NotificationChannel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "webhook" | "email"
	Enabled bool   `json:"enabled"`

	// Webhook-only.
	WebhookURL    string `json:"webhook_url,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"` // optional HMAC-SHA256 signature header
	// WebhookFormat picks the payload shape:
	//   ""        → auto-detect by URL host (default; matches v5.4.0)
	//   "discord" → rich embed with color + fields
	//   "slack"   → attachments envelope with color + fields
	//   "generic" → raw event JSON, useful for n8n / Zapier / custom listeners
	WebhookFormat string `json:"webhook_format,omitempty"`

	// Email-only. SMTP creds; if SMTPHost blank use the server's default
	// (set once on the global config).
	EmailTo   string `json:"email_to,omitempty"` // comma-separated
	EmailFrom string `json:"email_from,omitempty"`

	// EventTypes (v5.7) is this channel's event subscription. When
	// non-empty it overrides the global NotificationConfig.EventTypes
	// for this channel — so operators can route e.g. "migration.failed"
	// to a PagerDuty webhook and "vm.crashed" to a quieter Discord
	// channel. When empty, the channel inherits the global set (old
	// broadcast behaviour, no breaking change).
	EventTypes map[string]bool `json:"event_types,omitempty"`

	// Observability.
	LastSendAt    string `json:"last_send_at,omitempty"`
	LastSendError string `json:"last_send_error,omitempty"`
	LastSendCount int64  `json:"last_send_count,omitempty"`
}

// NotificationConfig (v5.4) wires channels to event types. EventTypes
// is a map of event-type → enabled bool; the dispatcher fans every
// fired event out to every enabled channel whose event type is true.
// Keep the model dead-simple: per-channel routing was rejected as
// over-engineering for a homelab fleet of <100 VMs.
type NotificationConfig struct {
	Channels   []*NotificationChannel `json:"channels"`
	EventTypes map[string]bool        `json:"event_types"` // "vm.crashed", "host.offline", etc.

	// Global SMTP defaults — channel-level overrides win.
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUser     string `json:"smtp_user,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	SMTPFrom     string `json:"smtp_from,omitempty"`
	SMTPStartTLS bool   `json:"smtp_starttls,omitempty"`
}

// NotificationEvent is one fired notification handed to the dispatcher.
type NotificationEvent struct {
	Type     string            `json:"type"`    // canonical event key, e.g. "vm.crashed"
	Title    string            `json:"title"`   // short subject line
	Message  string            `json:"message"` // body / context
	Resource string            `json:"resource,omitempty"`
	Severity string            `json:"severity"` // "info" | "warning" | "critical"
	At       time.Time         `json:"at"`
	Detail   map[string]string `json:"detail,omitempty"`
}

// MetricsConfig (v5.13) configures the InfluxDB v2 telemetry sink.
// Disabled by default; writes host + VM samples + migration
// outcomes to an external Influx bucket so operators can build
// Grafana dashboards without scraping the API.
type MetricsConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"` // e.g. "https://influx.example.com:8086"
	Org     string `json:"org,omitempty"`
	Bucket  string `json:"bucket,omitempty"`
	Token   string `json:"token,omitempty"`

	// Measurement-name prefix. Defaults to "kvm_center_". Avoids
	// collisions on shared Influx buckets.
	MeasurementPrefix string `json:"measurement_prefix,omitempty"`

	// Batch tuning — matches AuditShipping for operator familiarity.
	BatchSize    int `json:"batch_size,omitempty"`
	FlushSeconds int `json:"flush_seconds,omitempty"`

	// Observability.
	LastWriteAt    string `json:"last_write_at,omitempty"`
	LastWriteError string `json:"last_write_error,omitempty"`
	LastWriteCount int64  `json:"last_write_count,omitempty"`
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
	Action    string            `json:"action"`             // "vm.start", "role.update", "tls.upload", etc.
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
	DiskType string `json:"disk_type,omitempty"` // "qcow2", "raw"
	Firmware string `json:"firmware,omitempty"`  // "bios", "efi", "efi+secureboot"
	// SnapshotsBlockedReason is non-empty when the agent detects the VM
	// is incompatible with `virsh snapshot-create` (e.g. pflash+raw nvram).
	// UI reads this to disable Take Snapshot and surface the cause.
	SnapshotsBlockedReason string        `json:"snapshots_blocked_reason,omitempty"`
	MachineType            string        `json:"machine_type,omitempty"` // "pc-q35-rhel9.6.0"
	CPUMode                string        `json:"cpu_mode,omitempty"`     // "host-passthrough", "custom"
	Autostart              bool          `json:"autostart"`
	HasTPM                 bool          `json:"has_tpm"`
	TPMVersion             string        `json:"tpm_version,omitempty"`
	VideoModel             string        `json:"video_model,omitempty"`
	BootDev                string        `json:"boot_dev,omitempty"`
	AllDisks               []VMDisk      `json:"all_disks,omitempty"`
	PCIDevices             []VMPCIDevice `json:"pci_devices,omitempty"`

	// Guest-side fields. Populated by qemu-guest-agent (via hypervisor) or
	// kvm-tools daemon (via POST /api/guest-telemetry). Empty when no agent.
	GuestAgent           bool             `json:"guest_agent"`            // true if any guest source responded
	GuestSource          string           `json:"guest_source,omitempty"` // "qemu-ga" | "kvm-tools"
	GuestOS              string           `json:"guest_os,omitempty"`
	GuestHostname        string           `json:"guest_hostname,omitempty"`
	GuestKernel          string           `json:"guest_kernel,omitempty"`
	GuestUptime          string           `json:"guest_uptime,omitempty"`
	GuestIPs             []string         `json:"guest_ips,omitempty"`
	GuestFS              []GuestFS        `json:"guest_fs,omitempty"`
	GuestInterfaces      []GuestInterface `json:"guest_interfaces,omitempty"`
	KVMToolsVersion      string           `json:"kvm_tools_version,omitempty"`
	KVMToolsUpdateQueued bool             `json:"kvm_tools_update_queued,omitempty"`

	// User-editable, persisted in server store (rehydrated by UUID).
	Notes string `json:"notes,omitempty"`
}

// GuestInterface is one network interface reported by the in-guest
// kvm-tools daemon, classified so the UI can flag Docker bridges,
// VPN tunnels, and container veth pairs separately from the primary
// NIC. Loopback is filtered out at collection time.
type GuestInterface struct {
	Name  string   `json:"name"` // eth0, docker0, br-1a2b…, tailscale0
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
	Target     string  `json:"target"`    // "vda", "sda"
	Device     string  `json:"device"`    // "disk", "cdrom"
	DiskType   string  `json:"disk_type"` // "qcow2", "raw"
	Bus        string  `json:"bus"`       // "virtio", "scsi", "sata"
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
	VMUUID          string           `json:"vm_uuid"`
	Hostname        string           `json:"hostname"`
	OS              string           `json:"os"`
	Kernel          string           `json:"kernel"`
	Uptime          string           `json:"uptime"`
	CPUCount        int              `json:"cpu_count"`
	MemTotalMB      uint64           `json:"mem_total_mb"`
	MemUsedMB       uint64           `json:"mem_used_mb"`
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
	Name         string  `json:"name"`
	HostID       string  `json:"host_id"`
	Path         string  `json:"path"`
	CapacityGB   float64 `json:"capacity_gb"`
	AllocationGB float64 `json:"allocation_gb"`
	AvailableGB  float64 `json:"available_gb"`
	State        string  `json:"state"`
	PoolType     string  `json:"pool_type"`
}

// TelemetryPayload is the JSON body sent by an agent to POST /api/telemetry.
type TelemetryPayload struct {
	HostID       string        `json:"host_id"`
	IP           string        `json:"ip"`
	AgentPort    int           `json:"agent_port"`
	OS           string        `json:"os"`
	KernelVer    string        `json:"kernel_ver"`
	LibvirtVer   string        `json:"libvirt_ver"`
	CPUUsage     float64       `json:"cpu_usage"`
	CPUCount     int           `json:"cpu_count"`
	MemUsage     float64       `json:"mem_usage"`
	MemTotalMB   uint64        `json:"mem_total_mb"`
	MemFreeMB    uint64        `json:"mem_free_mb"`
	Uptime       string        `json:"uptime"`
	AgentVersion string        `json:"agent_version,omitempty"`
	VMs          []VM          `json:"vms"`
	Storage      []StoragePool `json:"storage"`
	Timestamp    time.Time     `json:"timestamp"`

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
	TOTPSecret  string `json:"totp_secret,omitempty"` // omitted from API responses via sanitizeUser
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
	ID            string `json:"id"`
	Kind          string `json:"kind"` // "google" | "github" | "microsoft" | "oidc"
	Name          string `json:"name"` // display label on the login button
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer,omitempty"` // if set, discovery fills the URLs below
	AuthURL       string `json:"auth_url,omitempty"`
	TokenURL      string `json:"token_url,omitempty"`
	UserinfoURL   string `json:"userinfo_url,omitempty"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"` // omitted from API responses
	Scopes        string `json:"scopes,omitempty"`        // space-separated, e.g. "openid email profile"
	DefaultRoleID string `json:"default_role_id,omitempty"`
	SubjectClaim  string `json:"subject_claim,omitempty"`  // default "sub"; GitHub uses "id"
	EmailClaim    string `json:"email_claim,omitempty"`    // default "email"
	UsernameClaim string `json:"username_claim,omitempty"` // default "preferred_username" then "email"

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
	URL                string `json:"url"`                     // ldap://host:389 or ldaps://host:636
	BindDN             string `json:"bind_dn,omitempty"`       // service account used for user lookup; blank → bind directly as user
	BindPassword       string `json:"bind_password,omitempty"` // omitted from API responses
	UserBaseDN         string `json:"user_base_dn"`            // DN under which users live
	UserFilter         string `json:"user_filter"`             // e.g. "(sAMAccountName=%s)" — %s is the submitted username
	UsernameAttr       string `json:"username_attr,omitempty"` // default: sAMAccountName (AD) / uid (OpenLDAP)
	EmailAttr          string `json:"email_attr,omitempty"`    // default: mail
	StartTLS           bool   `json:"start_tls,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

// SAMLConfig holds the minimum required to drive an SP-initiated SAML 2.0
// sign-in flow against an external IdP. Either MetadataURL alone (we'll
// fetch + parse on demand) or the manual trio (IDPEntityID, SSOURL,
// Certificate) must be populated.
type SAMLConfig struct {
	MetadataURL string `json:"metadata_url,omitempty"`
	IDPEntityID string `json:"idp_entity_id,omitempty"`
	SSOURL      string `json:"sso_url,omitempty"`
	Certificate string `json:"certificate,omitempty"` // PEM-encoded IdP signing certificate

	// SP (our) side. When blank we synthesize entity_id = <base_url>/api/auth/saml/metadata?provider=<id>
	SPEntityID string `json:"sp_entity_id,omitempty"`

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

// MigrationGrant (v5.11) is a short-lived, single-use authorization
// token the control plane issues for one specific migration. Source
// agent forwards the envelope to the destination; destination
// validates the signature offline against the server's Ed25519 public
// key before letting libvirt accept the migration.
//
// The envelope ships as a JSON object carried in a single
// base64-encoded string so it fits cleanly in command params. See
// server/grants.go for signing; agent/grants.go for validation.
type MigrationGrant struct {
	ID        string `json:"id"`      // uuid-like
	Source    string `json:"src"`     // source host ID
	Dest      string `json:"dst"`     // destination host ID
	VMUUID    string `json:"vm_uuid"` // bound VM
	VMName    string `json:"vm_name,omitempty"`
	IssuedAt  int64  `json:"iat"`  // unix seconds
	ExpiresAt int64  `json:"exp"`  // unix seconds — typically 5 min
	Mode      string `json:"mode"` // "live" | "offline"
}

// SignedGrant is the envelope ready for transport. Payload is the
// JSON encoding of MigrationGrant; Signature is the Ed25519 signature
// over Payload using the server's grant-signing key.
type SignedGrant struct {
	Payload   []byte `json:"payload"`   // canonical JSON bytes of MigrationGrant
	Signature []byte `json:"signature"` // Ed25519 signature over Payload
}

// HypervisorCert (v5.10) records one leaf certificate issued by the
// cluster CA to a hypervisor agent. The server tracks issuance and
// revocation so operators can audit the PKI surface from
// Settings → TLS → Hypervisor PKI.
//
// The private key never leaves the hypervisor — only the CSR's public
// material reaches the server. RevokedAt being non-zero means the
// cert is on the revocation list; new migrations that carry this
// certificate's subject CN will be refused (v5.10.1+).
type HypervisorCert struct {
	HostID       string    `json:"host_id"`
	SerialHex    string    `json:"serial_hex"`
	Fingerprint  string    `json:"fingerprint"` // SHA-256 of DER, colon-hex
	Subject      string    `json:"subject,omitempty"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	IssuedAt     time.Time `json:"issued_at"`
	IssuedBy     string    `json:"issued_by,omitempty"` // user ID that triggered enrollment
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
	RevokedBy    string    `json:"revoked_by,omitempty"`
	RevokeReason string    `json:"revoke_reason,omitempty"`
}

// APIToken (v5.5) is a long-lived bearer credential for scripted use
// of the kvm-center API. The full token is shown to the operator
// exactly once at creation; the server only persists a SHA-256 hash
// of the secret. Tokens inherit the owner user's permissions —
// per-token scoping is intentionally out of scope for v5.5 to keep
// the model small.
//
// Token wire format: "kvmc_<id>.<secret>"
//
//	<id>     — 8 hex chars, public, used to look up the row
//	<secret> — 48 hex chars, high-entropy random, never persisted
type APIToken struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
	// Kind distinguishes user-minted tokens (self-service, default 30d
	// expiry, managed from the user menu) from system-level tokens
	// (admin-minted, longer-lived, managed from Settings). Both types
	// still inherit their owner's permissions — "system" just means the
	// owner is typically an admin used as a service account.
	Kind       string    `json:"kind"`           // "user" | "system"
	Hash       string    `json:"hash,omitempty"` // never returned via API
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Revoked    bool      `json:"revoked"`
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

	// SetupComplete gates the first-boot /setup wizard (v5.20). When
	// false the SPA redirects admins into the wizard so they can
	// optionally configure notifications, metrics, and enrol the first
	// hypervisor. Back-filled to true on upgrades that already have an
	// admin user so existing installs don't see the wizard.
	SetupComplete    bool      `json:"setup_complete"`
	SetupCompletedAt time.Time `json:"setup_completed_at,omitempty"`

	// PublicURL is the operator-configured canonical URL clients use to
	// reach this server (e.g. https://kvm.example.com). Threaded into
	// Deployment page install snippets and notification webhook deep
	// links. Empty means "fall back to window.location in the SPA and
	// disable deep links in webhooks".
	PublicURL string `json:"public_url,omitempty"`

	// SSHStrictHostKeyChecking (v5.44.0) — when true, the agent's
	// SSH-transport migration paths (offline SCP, fallback live
	// migrate via qemu+ssh) dial with `StrictHostKeyChecking=yes`
	// and `UserKnownHostsFile=/etc/kvm-center/ssh_known_hosts`.
	// Operators must populate that file before flipping the flag —
	// otherwise SSH migrations refuse with "host key verification
	// failed". Default false to preserve existing behaviour while
	// audit-trail (v5.37.1 Tier 2 #8) keeps SSH-transport choices
	// visible. Recommended on shared / multi-tenant networks; on a
	// dedicated management VLAN the existing posture is acceptable.
	SSHStrictHostKeyChecking bool `json:"ssh_strict_host_key_checking,omitempty"`

	// SSHKnownHosts (v5.46.0) — operator-managed known_hosts content
	// pushed to every agent during install + on demand. When
	// SSHStrictHostKeyChecking is on, agents persist this verbatim
	// to /var/lib/kvm-migrator/.ssh/known_hosts so SSH migrations
	// pass host-key verification without manual fleet edits.
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	// GRPCRequireClientCert (v5.46.0) — when true, the gRPC stream
	// interceptor refuses streams that don't present a CA-signed
	// client cert (rather than falling back to the metadata header
	// the way v5.37.1+v5.44.0 do). Flip on once every agent is on
	// v5.44.0+ AND has run install-agent.sh (which writes the leaf
	// cert to /etc/pki/libvirt/clientcert.pem). Off by default so
	// existing fleets don't break on upgrade.
	GRPCRequireClientCert bool `json:"grpc_require_client_cert,omitempty"`
}

// BackupSchedule (v5.43.0) gates the in-process scheduler that
// runs `ExportSnapshot` periodically and writes the bundled tarball
// to `DestPath`, retaining the last `RetainCount` files. The
// scheduler ticks every minute in a background goroutine and fires
// when `time.Since(LastRunAt) >= EveryHours`. Disabled by default
// — operator opts in from Settings → Backup.
type BackupSchedule struct {
	Enabled              bool      `json:"enabled"`
	EveryHours           int       `json:"every_hours,omitempty"`
	CronExpr             string    `json:"cron_expr,omitempty"`
	DestPath             string    `json:"dest_path"`
	RetainCount          int       `json:"retain_count"`
	EncryptionPassphrase string    `json:"encryption_passphrase,omitempty"`
	LastRunAt            time.Time `json:"last_run_at,omitempty"`
	LastRunErr           string    `json:"last_run_err,omitempty"`
	LastRunSize          int64     `json:"last_run_size_bytes,omitempty"`
	NextRunAt            time.Time `json:"next_run_at,omitempty"`
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
	Mode        string `json:"mode"`                // "offline" | "live"
	Transport   string `json:"transport,omitempty"` // "tls" | "ssh" (v5.10.7)
	Status      string `json:"status"`              // "running" | "complete" | "failed"
	StartedAt   int64  `json:"started_at"`          // unix seconds
	CompletedAt int64  `json:"completed_at,omitempty"`
	ElapsedSec  int64  `json:"elapsed_sec,omitempty"`
	DiskSizeMB  int64  `json:"disk_size_mb,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Error       string `json:"error,omitempty"`
	Progress    int    `json:"progress"`
	Phase       string `json:"phase,omitempty"`
}

// CommandRequest is sent by the server to an agent's POST /command endpoint.
// RequestID (v5.32.17+) uniquely identifies this invocation — the agent
// keeps a short-TTL outcome cache keyed on it, so a retry after a
// dropped response returns the cached reply instead of re-executing
// the command. Empty RequestID disables idempotency (pre-5.32.17
// servers + curl-by-hand).
type CommandRequest struct {
	RequestID string            `json:"request_id,omitempty"`
	Action    string            `json:"action"` // START, STOP, RESTART, SNAPSHOT, MIGRATE
	VMName    string            `json:"vm_name"`
	VMUUID    string            `json:"vm_uuid"`
	Params    map[string]string `json:"params,omitempty"`
}

// CommandResponse is returned by the agent after executing a command.
// RequestID (v5.32.17+) echoes the value from the CommandRequest. When
// Replayed is true, this response was served from the agent's outcome
// cache — the command already executed on a prior call with the same
// request_id.
type CommandResponse struct {
	RequestID string `json:"request_id,omitempty"`
	Replayed  bool   `json:"replayed,omitempty"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	// RequiresReboot flags commands (EDIT_VM today) that were
	// accepted but couldn't be applied live. The UI reads this to
	// prompt the operator to shut down + restart the VM.
	RequiresReboot bool `json:"requires_reboot,omitempty"`
	// UpdatedFields carries the VM's post-edit field values so the
	// server can refresh its in-memory cache without waiting for
	// the next 30 s telemetry tick. EDIT_VM populates this; other
	// actions leave it empty. Keys match the lowercase JSON field
	// names on VM (cpu_count, mem_total_mb, boot_dev, cpu_mode,
	// video_model, autostart).
	UpdatedFields map[string]any `json:"updated_fields,omitempty"`
}

// APICommandRequest is sent by the web UI to POST /api/command on the server.
// RequestID is optional: the client may supply one to turn a sequence
// of retries into a single logical operation; when empty, the server
// mints one and echoes it in the response header `X-Request-Id`.
type APICommandRequest struct {
	RequestID string            `json:"request_id,omitempty"`
	HostID    string            `json:"host_id"`
	VMName    string            `json:"vm_name"`
	VMUUID    string            `json:"vm_uuid"`
	Action    string            `json:"action"`
	Params    map[string]string `json:"params,omitempty"`
}

// CommandLogEntry records every mutating command the server
// dispatched so a restart + any dropped response can be reconciled
// from this log alone. Written BEFORE the agent call and updated
// when the agent responds. TTL-expired 30 days after terminal.
//
// Tier S #4, v5.32.17.
type CommandLogEntry struct {
	RequestID   string            `json:"request_id" bson:"_id"`
	Action      string            `json:"action" bson:"action"`
	HostID      string            `json:"host_id" bson:"host_id"`
	VMUUID      string            `json:"vm_uuid,omitempty" bson:"vm_uuid,omitempty"`
	VMName      string            `json:"vm_name,omitempty" bson:"vm_name,omitempty"`
	Params      map[string]string `json:"params,omitempty" bson:"params,omitempty"`
	ActorID     string            `json:"actor_id,omitempty" bson:"actor_id,omitempty"`
	SubmittedAt time.Time         `json:"submitted_at" bson:"submitted_at"`
	AppliedAt   *time.Time        `json:"applied_at,omitempty" bson:"applied_at,omitempty"`
	// Outcome is "pending" while the command is in flight, then
	// "success" / "failed" when the agent returns, or "unknown" when
	// the server crashed mid-flight and the boot reconciler couldn't
	// determine terminal state.
	Outcome    string     `json:"outcome" bson:"outcome"`
	StatusCode int        `json:"status_code,omitempty" bson:"status_code,omitempty"`
	Message    string     `json:"message,omitempty" bson:"message,omitempty"`
	Replayed   bool       `json:"replayed,omitempty" bson:"replayed,omitempty"`
	ExpiresAt  *time.Time `json:"-" bson:"expires_at,omitempty"`
	// InflightUntil (v5.47.2+) is the deadline by which a "pending"
	// entry must reach a terminal state. Set when the server's HTTP
	// call to the agent times out or hits a transport error: instead
	// of marking the entry failed (which lies when the agent actually
	// finished the work), we leave it pending until either the
	// reconciler resolves it or this deadline passes — at which point
	// the boot/periodic reconciler flips it to "unknown".
	InflightUntil time.Time `json:"inflight_until,omitempty" bson:"inflight_until,omitempty"`
	// LastPolledAt (v5.47.2+) is the last time the reconciler queried
	// the agent's /command-outcome endpoint for this entry. Used to
	// rate-limit polling without holding the command-log mutex across
	// HTTP calls.
	LastPolledAt time.Time `json:"last_polled_at,omitempty" bson:"last_polled_at,omitempty"`
}
