// Code generated from exact saved source c018b31b74d7d75ed8c49921dc0812bf5999d81f; data declarations only.
// Synthetic fixture schema, not executable legacy services or a compatibility claim.
package legacyfixture

import "time"

type APIToken struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`

	Kind       string    `json:"kind"`
	Hash       string    `json:"hash,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Revoked    bool      `json:"revoked"`

	Scopes []string `json:"scopes,omitempty"`
}

type AffinityRule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	Kind string `json:"kind"`

	Strength string `json:"strength"`

	VMs []string `json:"vms"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type Alarm struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	Metric string `json:"metric"`

	Threshold float64 `json:"threshold"`

	ForSeconds int `json:"for_seconds"`

	Severity string `json:"severity"`

	Hosts []string `json:"hosts,omitempty"`
	VMs   []string `json:"vms,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	FiringOn []string `json:"firing_on,omitempty" bson:"-"`
}

type AuditEvent struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	ActorID   string            `json:"actor_id,omitempty" bson:"actor_id,omitempty"`
	ActorName string            `json:"actor_name,omitempty"`
	ActorIP   string            `json:"actor_ip,omitempty"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
	Success   bool              `json:"success"`
	Error     string            `json:"error,omitempty"`
}

type AuditShipping struct {
	Enabled bool `json:"enabled"`

	Type string `json:"type,omitempty"`

	Endpoint string `json:"endpoint,omitempty"`

	BearerToken string `json:"bearer_token,omitempty"`
	BasicAuth   string `json:"basic_auth,omitempty"`

	SyslogFacility int    `json:"syslog_facility,omitempty"`
	SyslogAppName  string `json:"syslog_app_name,omitempty"`

	BatchSize    int `json:"batch_size,omitempty"`
	FlushSeconds int `json:"flush_seconds,omitempty"`

	LastShipAt    string `json:"last_ship_at,omitempty"`
	LastShipError string `json:"last_ship_error,omitempty"`
	LastShipCount int64  `json:"last_ship_count,omitempty"`
}

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

	Outcome    string     `json:"outcome" bson:"outcome"`
	StatusCode int        `json:"status_code,omitempty" bson:"status_code,omitempty"`
	Message    string     `json:"message,omitempty" bson:"message,omitempty"`
	Replayed   bool       `json:"replayed,omitempty" bson:"replayed,omitempty"`
	ExpiresAt  *time.Time `json:"-" bson:"expires_at,omitempty"`

	InflightUntil time.Time `json:"inflight_until,omitempty" bson:"inflight_until,omitempty"`

	LastPolledAt time.Time `json:"last_polled_at,omitempty" bson:"last_polled_at,omitempty"`

	LastProgress *CommandProgressFrame `json:"last_progress,omitempty" bson:"last_progress,omitempty"`
}

type CommandProgressFrame struct {
	RequestID   string `json:"request_id"`
	HostID      string `json:"host_id,omitempty"`
	Action      string `json:"action,omitempty"`
	VMUUID      string `json:"vm_uuid,omitempty"`
	VMName      string `json:"vm_name,omitempty"`
	Phase       string `json:"phase,omitempty"`
	ProgressPct int32  `json:"progress_pct"`
	Detail      string `json:"detail,omitempty"`
	ReportedAt  int64  `json:"reported_at,omitempty"`
}

type DiscordGateway struct {
	Enabled bool `json:"enabled"`

	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	TLS       bool   `json:"tls"`
	VerifyTLS bool   `json:"verify_tls"`

	RegistrationID string `json:"registration_id,omitempty"`
	WebhookID      string `json:"webhook_id,omitempty"`

	Token string `json:"-" bson:"token,omitempty"`

	Status string `json:"status,omitempty"`

	LastHealthAt    string `json:"last_health_at,omitempty"`
	LastHealthError string `json:"last_health_error,omitempty"`
	LastSendAt      string `json:"last_send_at,omitempty"`
	LastSendError   string `json:"last_send_error,omitempty"`
	LastSendCount   int64  `json:"last_send_count,omitempty"`
	DroppedEvents   int64  `json:"dropped_events,omitempty"`

	PendingSince string `json:"pending_since,omitempty"`

	ShipAuditEvents bool `json:"ship_audit_events"`

	ShipAuditFailedOnly bool `json:"ship_audit_failed_only"`
}

type ExternalIdentity struct {
	ProviderID string    `json:"provider_id"`
	Subject    string    `json:"subject"`
	Email      string    `json:"email,omitempty"`
	LinkedAt   time.Time `json:"linked_at"`
}

type Features struct {
	DisableVMCreate      bool `json:"disable_vm_create"`
	DisableVMDelete      bool `json:"disable_vm_delete"`
	DisableSnapshots     bool `json:"disable_snapshots"`
	DisableConsole       bool `json:"disable_console"`
	DisableMigrations    bool `json:"disable_migrations"`
	DisableStorageUpload bool `json:"disable_storage_upload"`
	DisableStorageDelete bool `json:"disable_storage_delete"`

	SetupComplete    bool      `json:"setup_complete"`
	SetupCompletedAt time.Time `json:"setup_completed_at,omitempty"`

	PublicURL string `json:"public_url,omitempty"`

	SSHStrictHostKeyChecking bool `json:"ssh_strict_host_key_checking"`

	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	NotifyOnTaskFailure bool `json:"notify_on_task_failure"`

	GRPCRequireClientCert bool `json:"grpc_require_client_cert"`

	PhantomHostPruneAfterDays int `json:"phantom_host_prune_after_days,omitempty"`
}

type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	RoleIDs     []string  `json:"role_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

type GuestFS struct {
	Mountpoint string  `json:"mountpoint"`
	FSType     string  `json:"fstype"`
	TotalGB    float64 `json:"total_gb"`
	UsedGB     float64 `json:"used_gb"`
}

type GuestGrowResult struct {
	Device string `json:"device"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type GuestInterface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac,omitempty"`
	Addrs []string `json:"addrs,omitempty"`

	Kind string `json:"kind"`
}

type GuestTelemetryPayload struct {
	VMUUID   string `json:"vm_uuid"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`

	Arch            string           `json:"arch,omitempty"`
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

	GrowResult *GuestGrowResult `json:"grow_result,omitempty"`
}

type Host struct {
	ID        string `json:"id"`
	IP        string `json:"ip"`
	AgentPort int    `json:"agent_port"`

	AgentTLSPort int        `json:"agent_tls_port,omitempty" bson:"agent_tls_port,omitempty"`
	CPUUsage     float64    `json:"cpu_usage"`
	CPUCount     int        `json:"cpu_count"`
	MemUsage     float64    `json:"mem_usage"`
	MemTotalMB   uint64     `json:"mem_total_mb"`
	MemFreeMB    uint64     `json:"mem_free_mb"`
	VMCount      int        `json:"vm_count"`
	Status       HostStatus `json:"status"`
	LastSeen     time.Time  `json:"last_seen"`
	OS           string     `json:"os"`
	KernelVer    string     `json:"kernel_ver"`
	LibvirtVer   string     `json:"libvirt_ver"`
	Uptime       string     `json:"uptime"`

	AgentVersion string `json:"agent_version,omitempty"`

	CPUModel       string          `json:"cpu_model,omitempty"`
	Sockets        int             `json:"sockets,omitempty"`
	CoresPerSocket int             `json:"cores_per_socket,omitempty"`
	ThreadsPerCore int             `json:"threads_per_core,omitempty"`
	NUMANodes      int             `json:"numa_nodes,omitempty"`
	PCIDevices     []HostPCIDevice `json:"host_pci_devices,omitempty"`
	GPUs           []string        `json:"gpus,omitempty"`

	MaintenanceMode      bool      `json:"maintenance_mode,omitempty"`
	MaintenanceEnteredAt time.Time `json:"maintenance_entered_at,omitempty"`
	MaintenanceBy        string    `json:"maintenance_by,omitempty"`
	MaintenanceReason    string    `json:"maintenance_reason,omitempty"`

	MTLSReady bool `json:"mtls_ready,omitempty"`

	MigratorPubkey string `json:"migrator_pubkey,omitempty"`

	SSHHostKeys []string `json:"ssh_host_keys,omitempty"`

	ExpectedDown       bool      `json:"expected_down,omitempty"`
	ExpectedDownReason string    `json:"expected_down_reason,omitempty"`
	ExpectedDownBy     string    `json:"expected_down_by,omitempty"`
	ExpectedDownSetAt  time.Time `json:"expected_down_set_at,omitempty"`
	ExpectedReturnAt   time.Time `json:"expected_return_at,omitempty"`
}

type HostPCIDevice struct {
	Address     string `json:"address"`
	Type        string `json:"type"`
	Description string `json:"description"`
	IOMMUGroup  int    `json:"iommu_group"`
	InUseByVM   string `json:"in_use_by_vm,omitempty"`
}

type HostStatus string

type HypervisorCert struct {
	HostID       string    `json:"host_id"`
	SerialHex    string    `json:"serial_hex"`
	Fingerprint  string    `json:"fingerprint"`
	Subject      string    `json:"subject,omitempty"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	IssuedAt     time.Time `json:"issued_at"`
	IssuedBy     string    `json:"issued_by,omitempty"`
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
	RevokedBy    string    `json:"revoked_by,omitempty"`
	RevokeReason string    `json:"revoke_reason,omitempty"`
}

type IdentityProvider struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer,omitempty"`
	AuthURL       string `json:"auth_url,omitempty"`
	TokenURL      string `json:"token_url,omitempty"`
	UserinfoURL   string `json:"userinfo_url,omitempty"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	Scopes        string `json:"scopes,omitempty"`
	DefaultRoleID string `json:"default_role_id,omitempty"`
	SubjectClaim  string `json:"subject_claim,omitempty"`
	EmailClaim    string `json:"email_claim,omitempty"`
	UsernameClaim string `json:"username_claim,omitempty"`

	LDAP *LDAPConfig `json:"ldap,omitempty"`

	SAML *SAMLConfig `json:"saml,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

type LDAPConfig struct {
	URL                string `json:"url"`
	BindDN             string `json:"bind_dn,omitempty"`
	BindPassword       string `json:"bind_password,omitempty"`
	UserBaseDN         string `json:"user_base_dn"`
	UserFilter         string `json:"user_filter"`
	UsernameAttr       string `json:"username_attr,omitempty"`
	EmailAttr          string `json:"email_attr,omitempty"`
	StartTLS           bool   `json:"start_tls"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

type MetricsConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`
	Org     string `json:"org,omitempty"`
	Bucket  string `json:"bucket,omitempty"`
	Token   string `json:"token,omitempty"`

	MeasurementPrefix string `json:"measurement_prefix,omitempty"`

	BatchSize    int `json:"batch_size,omitempty"`
	FlushSeconds int `json:"flush_seconds,omitempty"`

	LastWriteAt    string `json:"last_write_at,omitempty"`
	LastWriteError string `json:"last_write_error,omitempty"`
	LastWriteCount int64  `json:"last_write_count,omitempty"`
}

type MigrationRecord struct {
	ID          string `json:"id"`
	VMName      string `json:"vm_name"`
	SourceHost  string `json:"source_host"`
	DestHost    string `json:"dest_host"`
	Mode        string `json:"mode"`
	Transport   string `json:"transport,omitempty"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"started_at" bson:"started_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	ElapsedSec  int64  `json:"elapsed_sec,omitempty"`
	DiskSizeMB  int64  `json:"disk_size_mb,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Error       string `json:"error,omitempty"`
	Progress    int    `json:"progress"`
	Phase       string `json:"phase,omitempty"`

	RequestID string `json:"request_id,omitempty"`
}

type NotificationChannel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`

	WebhookURL    string `json:"webhook_url,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`

	WebhookFormat string `json:"webhook_format,omitempty"`

	EmailTo   string `json:"email_to,omitempty"`
	EmailFrom string `json:"email_from,omitempty"`

	EventTypes map[string]bool `json:"event_types,omitempty"`

	LastSendAt    string `json:"last_send_at,omitempty"`
	LastSendError string `json:"last_send_error,omitempty"`
	LastSendCount int64  `json:"last_send_count,omitempty"`
}

type NotificationConfig struct {
	Channels   []*NotificationChannel `json:"channels"`
	EventTypes map[string]bool        `json:"event_types"`

	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUser     string `json:"smtp_user,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	SMTPFrom     string `json:"smtp_from,omitempty"`
	SMTPStartTLS bool   `json:"smtp_starttls"`
}

type PendingEnrollment struct {
	ID            string            `json:"id"`
	HostID        string            `json:"host_id"`
	CSR           string            `json:"csr"`
	Fingerprint   string            `json:"fingerprint"`
	SourceIP      string            `json:"source_ip"`
	SourceVersion string            `json:"source_version,omitempty"`
	SystemInfo    map[string]string `json:"system_info,omitempty"`

	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	Status string `json:"status"`

	PollTokenHash string `json:"-" bson:"poll_token_hash,omitempty"`

	ApprovedBy     string    `json:"approved_by,omitempty"`
	ApprovedAt     time.Time `json:"approved_at,omitempty"`
	IssuedSerial   string    `json:"issued_serial,omitempty"`
	IssuedNotAfter time.Time `json:"issued_not_after,omitempty"`

	IssuedCertPEM string    `json:"-" bson:"issued_cert_pem,omitempty"`
	IssuedCAPEM   string    `json:"-" bson:"issued_ca_pem,omitempty"`
	DeliveredAt   time.Time `json:"delivered_at,omitempty"`

	RejectedBy string    `json:"rejected_by,omitempty"`
	RejectedAt time.Time `json:"rejected_at,omitempty"`
	Reason     string    `json:"reason,omitempty"`

	AutoApproved bool `json:"auto_approved,omitempty"`
}

type RemovedHost struct {
	HostID    string    `json:"host_id"`
	RemovedAt time.Time `json:"removed_at"`
	RemovedBy string    `json:"removed_by,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	BuiltIn     bool      `json:"built_in"`
	CreatedAt   time.Time `json:"created_at"`
}

type SAMLConfig struct {
	MetadataURL string `json:"metadata_url,omitempty"`
	IDPEntityID string `json:"idp_entity_id,omitempty"`
	SSOURL      string `json:"sso_url,omitempty"`
	Certificate string `json:"certificate,omitempty"`

	SPEntityID string `json:"sp_entity_id,omitempty"`

	SubjectAttr  string `json:"subject_attr,omitempty"`
	EmailAttr    string `json:"email_attr,omitempty"`
	UsernameAttr string `json:"username_attr,omitempty"`
}

type Session struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at" bson:"expires_at"`
	UserAgent string    `json:"user_agent,omitempty"`
}

type Snapshot struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	CreationTime string `json:"creation_time"`
	State        string `json:"state"`
	Parent       string `json:"parent,omitempty"`
	IsCurrent    bool   `json:"is_current"`
}

type SnapshotPolicy struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	VMUUIDs       []string  `json:"vm_uuids"`
	EveryMinutes  int       `json:"every_minutes"`
	RetentionKeep int       `json:"retention_keep"`
	CreatedAt     time.Time `json:"created_at"`
	LastRunAt     time.Time `json:"last_run_at,omitempty"`
	LastRunError  string    `json:"last_run_error,omitempty"`
	LastRunCount  int       `json:"last_run_count,omitempty"`
}

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

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	PasswordHash string    `json:"password_hash,omitempty"`
	Enabled      bool      `json:"enabled"`
	RoleIDs      []string  `json:"role_ids"`
	GroupIDs     []string  `json:"group_ids"`
	CreatedAt    time.Time `json:"created_at"`
	LastLoginAt  time.Time `json:"last_login_at,omitempty"`

	TOTPSecret  string `json:"totp_secret,omitempty"`
	TOTPEnabled bool   `json:"totp_enabled"`

	ExternalIDs []ExternalIdentity `json:"external_ids,omitempty"`

	NotifyTaskSuccessActions []string `json:"notify_task_success_actions,omitempty"`
}

type VM struct {
	UUID          string      `json:"uuid"`
	Name          string      `json:"name"`
	HostID        string      `json:"host_id" bson:"host_id"`
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

	DiskType string `json:"disk_type,omitempty"`
	Firmware string `json:"firmware,omitempty"`

	SnapshotsBlockedReason string   `json:"snapshots_blocked_reason,omitempty"`
	MachineType            string   `json:"machine_type,omitempty"`
	CPUMode                string   `json:"cpu_mode,omitempty"`
	Autostart              bool     `json:"autostart"`
	HasTPM                 bool     `json:"has_tpm"`
	TPMVersion             string   `json:"tpm_version,omitempty"`
	VideoModel             string   `json:"video_model,omitempty"`
	BootDev                string   `json:"boot_dev,omitempty"`
	AllDisks               []VMDisk `json:"all_disks,omitempty"`

	HasPassthrough bool `json:"has_passthrough,omitempty"`

	Tags       []string      `json:"tags,omitempty" bson:"-"`
	PCIDevices []VMPCIDevice `json:"pci_devices,omitempty"`

	GuestAgent           bool             `json:"guest_agent"`
	GuestSource          string           `json:"guest_source,omitempty"`
	GuestOS              string           `json:"guest_os,omitempty"`
	GuestHostname        string           `json:"guest_hostname,omitempty"`
	GuestKernel          string           `json:"guest_kernel,omitempty"`
	GuestUptime          string           `json:"guest_uptime,omitempty"`
	GuestIPs             []string         `json:"guest_ips,omitempty"`
	GuestFS              []GuestFS        `json:"guest_fs,omitempty"`
	GuestInterfaces      []GuestInterface `json:"guest_interfaces,omitempty"`
	KVMToolsVersion      string           `json:"kvm_tools_version,omitempty"`
	KVMToolsUpdateQueued bool             `json:"kvm_tools_update_queued,omitempty"`

	Notes string `json:"notes,omitempty"`
}

type VMDisk struct {
	Target     string  `json:"target"`
	Device     string  `json:"device"`
	DiskType   string  `json:"disk_type"`
	Bus        string  `json:"bus"`
	Path       string  `json:"path"`
	CapacityGB float64 `json:"capacity_gb"`
	AllocGB    float64 `json:"alloc_gb"`
}

type VMNetwork struct {
	Interface string `json:"interface"`
	MAC       string `json:"mac"`
	Bridge    string `json:"bridge"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
}

type VMPCIDevice struct {
	SourceAddr string `json:"source_addr"`
	HostDesc   string `json:"host_desc"`
}

type VMState string

type guestEntry struct {
	Payload   GuestTelemetryPayload `json:"payload"`
	UpdatedAt time.Time             `json:"updated_at"`
}

type historyPoint struct {
	T          int64   `json:"t" bson:"t"`
	CPUAvg     float64 `json:"cpu_avg" bson:"cpu_avg"`
	CPUMax     float64 `json:"cpu_max" bson:"cpu_max"`
	MemAvg     float64 `json:"mem_avg" bson:"mem_avg"`
	MemMax     float64 `json:"mem_max" bson:"mem_max"`
	StorageAvg float64 `json:"storage_avg" bson:"storage_avg"`
	StorageMax float64 `json:"storage_max" bson:"storage_max"`
	VMs        int     `json:"vms" bson:"vms"`

	N int `json:"n" bson:"n"`
}

type persistData struct {
	Hosts            map[string]*Host             `json:"hosts"`
	VMs              map[string][]*VM             `json:"vms"`
	Storage          map[string][]*StoragePool    `json:"storage"`
	Alarms           []*Alarm                     `json:"alarms,omitempty"`
	AffinityRules    []*AffinityRule              `json:"affinity_rules,omitempty"`
	History          []persistedHistory           `json:"history,omitempty"`
	VMTags           map[string][]string          `json:"vm_tags,omitempty"`
	VMNotes          map[string]string            `json:"vm_notes"`
	GuestData        map[string]*guestEntry       `json:"guest_data"`
	Migrations       []*MigrationRecord           `json:"migrations,omitempty"`
	Users            map[string]*User             `json:"users,omitempty"`
	Groups           map[string]*Group            `json:"groups,omitempty"`
	Roles            map[string]*Role             `json:"roles,omitempty"`
	Sessions         map[string]*Session          `json:"sessions,omitempty"`
	IDPs             map[string]*IdentityProvider `json:"idps,omitempty"`
	Features         *Features                    `json:"features,omitempty"`
	Audit            []*AuditEvent                `json:"audit,omitempty"`
	AuditShipping    *AuditShipping               `json:"audit_shipping,omitempty"`
	SnapshotPolicies map[string]*SnapshotPolicy   `json:"snapshot_policies,omitempty"`
	Notifications    *NotificationConfig          `json:"notifications,omitempty"`
	Metrics          *MetricsConfig               `json:"metrics,omitempty"`
	APITokens        map[string]*APIToken         `json:"api_tokens,omitempty"`
	HypervisorCerts  map[string]*HypervisorCert   `json:"hypervisor_certs,omitempty"`
	CommandLog       map[string]*CommandLogEntry  `json:"command_log,omitempty"`

	ConsoleSecret string `json:"console_secret,omitempty"`

	BackupSchedule *BackupSchedule `json:"backup_schedule,omitempty"`

	DiscordGateway *DiscordGateway `json:"discord_gateway,omitempty"`

	RemovedHosts map[string]*RemovedHost `json:"removed_hosts,omitempty"`

	PendingEnrollments map[string]*PendingEnrollment `json:"pending_enrollments,omitempty"`

	Dirty map[string]bool `json:"-" bson:"-"`
}

type persistedHistory struct {
	Host  string                    `json:"host"`
	Tiers map[string][]historyPoint `json:"tiers"`
}
