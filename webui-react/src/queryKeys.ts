// Centralised React Query cache-key factory (P2 #3, v5.37.0;
// adoption finished v5.41.0).
//
// Pre-v5.37 every hook spelled its own cache-key array literal —
// `['vms']`, `['host-metrics', hostID]`, `['snapshots', hostID,
// vmName]` etc. Consistent in practice but spread across ~25 files;
// a future shape change (adding a tenant scope, key-namespacing for
// SSE invalidation, switching tuple order) would mean a 25-file
// diff.
//
// This factory is the single edit point. Each function returns an
// `as const` tuple so React Query's QueryKey constraints stay tight.

export const qk = {
  // --- Inventory --------------------------------------------------
  hosts: () => ['hosts'] as const,
  hostMetrics: (hostID: string) => ['host-metrics', hostID] as const,
  hostPools: (hostID: string) => ['host-pools', hostID] as const,
  vms: () => ['vms'] as const,
  vmCreateInfo: (hostID: string | null) => ['vm-create-info', hostID] as const,
  storage: () => ['storage'] as const,
  storageBrowse: (hostID: string, path: string) => ['storage-browse', hostID, path] as const,
  migrations: () => ['migrations'] as const,
  snapshots: (hostID: string, vmName: string) => ['snapshots', hostID, vmName] as const,
  snapshotPolicies: () => ['snapshot-policies'] as const,
  systemHealth: () => ['system-health'] as const,
  tasks: () => ['tasks'] as const,
  task: (id: string) => ['tasks', id] as const,

  // --- Identity / RBAC --------------------------------------------
  users: () => ['users'] as const,
  groups: () => ['groups'] as const,
  roles: () => ['roles'] as const,
  idps: () => ['idps'] as const,
  myTokens: () => ['my-tokens'] as const,
  apiTokens: () => ['api-tokens'] as const,

  // --- Settings / platform ----------------------------------------
  features: () => ['features'] as const,
  setupState: () => ['setup-state'] as const,
  notifications: () => ['notifications'] as const,
  metrics: () => ['metrics'] as const,
  metricsSettings: () => ['metrics-settings'] as const,
  auditShipping: () => ['audit-shipping'] as const,
  discordGateway: () => ['discord-gateway'] as const,
  tlsStatus: () => ['tls-status'] as const,
  caHypervisors: () => ['ca-hypervisors'] as const,
  dbStatus: () => ['db-status'] as const,
  kvmToolsLatest: () => ['kvm-tools-latest'] as const,

  // --- Docs -------------------------------------------------------
  docsTree: () => ['docs-tree'] as const,
  docsPage: (slug: string) => ['docs-page', slug] as const,
} as const;

// Export the inferred return-type unions so downstream code can be
// strictly-typed without re-spelling them. `qk.vms()` produces
// `readonly ['vms']`; QueryKey accepts that as a tuple.
export type QK = ReturnType<(typeof qk)[keyof typeof qk]>;
