// Permission strings — must stay in sync with models.AllPermissions
// in internal/models/models.go. The server validates token-scope
// requests against the same list and rejects unknown entries, so
// any addition here also needs to land in the Go constant block.

export interface PermissionDef {
  id: string;
  label: string;
  hint?: string;
}

export const PERMISSIONS: PermissionDef[] = [
  { id: 'admin',           label: 'admin',           hint: 'wildcard — grants everything' },
  { id: 'vm.view',         label: 'vm.view' },
  { id: 'vm.lifecycle',    label: 'vm.lifecycle',    hint: 'start / stop / restart / force-off' },
  { id: 'vm.create',       label: 'vm.create' },
  { id: 'vm.delete',       label: 'vm.delete' },
  { id: 'vm.migrate',      label: 'vm.migrate' },
  { id: 'vm.snapshot',     label: 'vm.snapshot' },
  { id: 'vm.console',      label: 'vm.console' },
  { id: 'vm.edit',         label: 'vm.edit',         hint: 'notes + hardware' },
  { id: 'host.view',       label: 'host.view' },
  { id: 'host.manage',     label: 'host.manage' },
  { id: 'storage.view',    label: 'storage.view' },
  { id: 'storage.upload',  label: 'storage.upload' },
  { id: 'storage.delete',  label: 'storage.delete' },
  { id: 'user.manage',     label: 'user.manage' },
  { id: 'tls.manage',      label: 'tls.manage' },
  { id: 'audit.view',      label: 'audit.view' },
];
