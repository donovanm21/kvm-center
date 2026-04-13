import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiDelete, apiGet, apiPost, apiPut } from '@/api/client';
import { useToast } from './useToast';
import type { Features } from '@/types';

/* ── Users ──────────────────────────────────────────────── */

export interface AdminUser {
  id: string;
  username: string;
  email?: string;
  enabled: boolean;
  role_ids?: string[];
  group_ids?: string[];
  totp_enabled?: boolean;
  created_at?: string;
  last_login_at?: string;
  external_ids?: { provider_id: string; subject: string; email?: string; linked_at?: string }[];
}

export interface AdminGroup {
  id: string;
  name: string;
  description?: string;
  role_ids?: string[];
  created_at?: string;
}

export interface AdminRole {
  id: string;
  name: string;
  description?: string;
  permissions: string[];
  built_in: boolean;
  created_at?: string;
}

export interface AdminRolesResponse {
  roles: AdminRole[];
  permissions: string[];
}

export interface AdminIDP {
  id: string;
  kind: string;
  name: string;
  enabled: boolean;
  issuer?: string;
  auth_url?: string;
  token_url?: string;
  userinfo_url?: string;
  client_id?: string;
  client_secret?: string; // only set when the admin is saving; server never echoes it
  scopes?: string;
  default_role_id?: string;
  subject_claim?: string;
  email_claim?: string;
  username_claim?: string;
  ldap?: LDAPConfig;
  saml?: SAMLConfig;
}

export interface LDAPConfig {
  url: string;
  bind_dn?: string;
  bind_password?: string;
  user_base_dn: string;
  user_filter: string;
  username_attr?: string;
  email_attr?: string;
  start_tls?: boolean;
  insecure_skip_verify?: boolean;
}

export interface SAMLConfig {
  metadata_url?: string;
  idp_entity_id?: string;
  sso_url?: string;
  certificate?: string;
  sp_entity_id?: string;
  subject_attr?: string;
  email_attr?: string;
  username_attr?: string;
}

export const useUsers = () =>
  useQuery({ queryKey: ['users'], queryFn: () => apiGet<AdminUser[]>('/api/users') });

export const useGroups = () =>
  useQuery({ queryKey: ['groups'], queryFn: () => apiGet<AdminGroup[]>('/api/groups') });

export const useRoles = () =>
  useQuery({ queryKey: ['roles'], queryFn: () => apiGet<AdminRolesResponse>('/api/roles') });

export const useIDPs = () =>
  useQuery({ queryKey: ['idps'], queryFn: () => apiGet<AdminIDP[]>('/api/idps') });

/* ── Feature toggles ────────────────────────────────────── */

export function useUpdateFeatures() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (f: Features) => apiPost<Features>('/api/settings/update', f),
    onSuccess: (data) => {
      qc.setQueryData(['features'], data);
      toast('success', 'Settings saved');
    },
    onError: (err) => toast('error', 'Save failed: ' + (err instanceof Error ? err.message : String(err))),
  });
}

/* ── User CRUD ──────────────────────────────────────────── */

export interface UserCreatePayload {
  username: string;
  email?: string;
  password: string;
  enabled: boolean;
  role_ids: string[];
  group_ids: string[];
}

export interface UserUpdatePayload {
  email?: string;
  password?: string;
  enabled?: boolean;
  role_ids?: string[];
  group_ids?: string[];
}

export function useCreateUser() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (p: UserCreatePayload) => apiPost<AdminUser>('/api/users/new', p),
    onSuccess: () => {
      toast('success', 'User created');
      qc.invalidateQueries({ queryKey: ['users'] });
    },
    onError: (err) => toast('error', 'Create failed: ' + (err instanceof Error ? err.message : String(err))),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ id, payload }: { id: string; payload: UserUpdatePayload }) =>
      apiPut<AdminUser>(`/api/users/${encodeURIComponent(id)}`, payload),
    onSuccess: () => {
      toast('success', 'User updated');
      qc.invalidateQueries({ queryKey: ['users'] });
    },
    onError: (err) => toast('error', 'Update failed: ' + (err instanceof Error ? err.message : String(err))),
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (id: string) => apiDelete(`/api/users/${encodeURIComponent(id)}`),
    onSuccess: () => {
      toast('success', 'User deleted');
      qc.invalidateQueries({ queryKey: ['users'] });
    },
    onError: (err) => toast('error', 'Delete failed: ' + (err instanceof Error ? err.message : String(err))),
  });
}
