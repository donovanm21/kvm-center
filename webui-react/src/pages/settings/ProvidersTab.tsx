import { useState } from 'react';

import { Modal } from '@/components/Modal';
import { ErrorBanner, Field, Select, TextArea, TextInput } from '@/components/FormBits';
import { useConfirm } from '@/hooks/useConfirm';
import {
  useCreateIDP,
  useDeleteIDP,
  useIDPs,
  useRoles,
  useUpdateIDP,
  type AdminIDP,
  type LDAPConfig,
  type SAMLConfig,
} from '@/hooks/useAdminResources';

type FormMode = { mode: 'new' } | { mode: 'edit'; idp: AdminIDP };
type Kind = 'google' | 'github' | 'microsoft' | 'oidc' | 'saml' | 'ldap';

/**
 * Presets mirror the v3 dropdown: pick a kind and the form seeds the
 * expected endpoints / scopes / claim names. Server still accepts any
 * overrides, but 90% of setups use the defaults.
 */
const PRESETS: Record<Kind, Partial<AdminIDP>> = {
  google: {
    kind: 'google',
    name: 'Google',
    issuer: 'https://accounts.google.com',
    scopes: 'openid email profile',
    subject_claim: 'sub',
    email_claim: 'email',
    username_claim: 'email',
  },
  github: {
    kind: 'github',
    name: 'GitHub',
    auth_url: 'https://github.com/login/oauth/authorize',
    token_url: 'https://github.com/login/oauth/access_token',
    userinfo_url: 'https://api.github.com/user',
    scopes: 'read:user user:email',
    subject_claim: 'id',
    email_claim: 'email',
    username_claim: 'login',
  },
  microsoft: {
    kind: 'microsoft',
    name: 'Microsoft',
    issuer: 'https://login.microsoftonline.com/common/v2.0',
    scopes: 'openid email profile',
    subject_claim: 'sub',
    email_claim: 'email',
    username_claim: 'preferred_username',
  },
  oidc: { kind: 'oidc', name: '', scopes: 'openid email profile' },
  saml: {
    kind: 'saml',
    name: 'SAML SSO',
    saml: { metadata_url: '', idp_entity_id: '', sso_url: '', certificate: '', sp_entity_id: '' },
  },
  ldap: {
    kind: 'ldap',
    name: 'Active Directory',
    ldap: {
      url: 'ldap://dc.example.com:389',
      user_base_dn: 'OU=Users,DC=example,DC=com',
      user_filter: '(sAMAccountName={username})',
      username_attr: 'sAMAccountName',
      email_attr: 'mail',
      start_tls: true,
    },
  },
};

export function ProvidersTab() {
  const idps = useIDPs();
  const confirmFn = useConfirm();
  const del = useDeleteIDP();
  const [form, setForm] = useState<FormMode | null>(null);
  const redirectURI = `${location.protocol}//${location.host}/api/auth/oidc/callback`;

  return (
    <>
      <div style={banner}>
        <strong style={{ color: 'var(--text-strong)' }}>OIDC redirect URI</strong> — configure this at your IdP:
        <code style={codeBlockStyle}>{redirectURI}</code>
      </div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn-primary-sm" onClick={() => setForm({ mode: 'new' })}>+ Add Provider</button>
      </div>
      <div className="panel" style={{ padding: 0, overflow: 'hidden' }}>
        {idps.isLoading && <Empty>Loading…</Empty>}
        {idps.error && <Empty danger>Failed: {(idps.error as Error).message}</Empty>}
        {idps.data && idps.data.length === 0 && !idps.isLoading && <Empty>No identity providers configured.</Empty>}
        {idps.data && idps.data.length > 0 && (
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Status</th>
                <th style={{ width: 160 }} />
              </tr>
            </thead>
            <tbody>
              {idps.data.map((p) => (
                <tr key={p.id}>
                  <td style={{ fontWeight: 500, color: 'var(--text-strong)' }}>{p.name}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{p.kind}</td>
                  <td>
                    <span className={`state-pill ${p.enabled ? 'running' : 'stopped'}`}>
                      {p.enabled ? 'enabled' : 'disabled'}
                    </span>
                  </td>
                  <td style={{ display: 'flex', gap: 6 }}>
                    <button className="btn-secondary-sm" onClick={() => setForm({ mode: 'edit', idp: p })}>Edit</button>
                    <button
                      className="btn-danger-sm"
                      onClick={async () => {
                        if (await confirmFn({ title: 'Delete identity provider?', body: <>Delete <code>{p.name}</code>? Linked accounts keep existing; they just can't sign in through it anymore.</> })) del.mutate(p.id);
                      }}
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {form && <ProviderFormModal form={form} onClose={() => setForm(null)} />}
    </>
  );
}

function ProviderFormModal({ form, onClose }: { form: FormMode; onClose: () => void }) {
  const isNew = form.mode === 'new';
  const existing = isNew ? undefined : form.idp;

  const [data, setData] = useState<Partial<AdminIDP>>(() =>
    isNew ? { enabled: true, ...PRESETS.google } : { ...existing, client_secret: '' },
  );
  const [err, setErr] = useState<string | null>(null);

  const roles = useRoles();
  const create = useCreateIDP();
  const update = useUpdateIDP();
  const busy = create.isPending || update.isPending;

  const kind = (data.kind ?? 'oidc') as Kind;
  const isLDAP = kind === 'ldap';
  const isSAML = kind === 'saml';

  const setKind = (k: Kind) => {
    if (!isNew) {
      setData((d) => ({ ...d, kind: k }));
      return;
    }
    // New: applying a preset overwrites matching fields but keeps the
    // admin's current "name" if they typed one already.
    setData((d) => ({
      ...d,
      ...PRESETS[k],
      name: d.name || PRESETS[k].name || '',
      enabled: d.enabled ?? true,
    }));
  };

  const setLDAP = (patch: Partial<LDAPConfig>) =>
    setData((d) => ({ ...d, ldap: { ...(d.ldap as LDAPConfig), ...patch } }));
  const setSAML = (patch: Partial<SAMLConfig>) =>
    setData((d) => ({ ...d, saml: { ...(d.saml as SAMLConfig), ...patch } }));

  const submit = async () => {
    setErr(null);
    if (!data.name?.trim()) {
      setErr('Display name is required.');
      return;
    }
    const payload: Partial<AdminIDP> = { ...data };
    // Server ignores empty client_secret on edit (keeps current). Pass
    // through as-is so the "leave blank to keep" affordance works.
    try {
      if (isNew) {
        await create.mutateAsync(payload);
      } else {
        await update.mutateAsync({ id: existing!.id, payload });
      }
      onClose();
    } catch {
      /* toast from hook */
    }
  };

  const rolesList = roles.data?.roles ?? [];

  return (
    <Modal
      open
      onClose={onClose}
      width={640}
      title={isNew ? 'Add Identity Provider' : `Edit — ${existing?.name}`}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn-primary-sm" onClick={submit} disabled={busy}>
            {busy ? 'Saving…' : isNew ? 'Create' : 'Save'}
          </button>
        </>
      }
    >
      <Field label="Kind">
        <Select value={kind} onChange={(e) => setKind(e.target.value as Kind)}>
          {(['google','github','microsoft','oidc','saml','ldap'] as Kind[]).map((k) => (
            <option key={k} value={k}>{k}</option>
          ))}
        </Select>
      </Field>

      <Field label="Display name">
        <TextInput
          value={data.name ?? ''}
          placeholder="e.g. Sign in with Google"
          onChange={(e) => setData((d) => ({ ...d, name: e.target.value }))}
        />
      </Field>

      {isLDAP && <LDAPFields ldap={data.ldap ?? {} as LDAPConfig} setLDAP={setLDAP} isNew={isNew} />}
      {isSAML && <SAMLFields saml={data.saml ?? {} as SAMLConfig} setSAML={setSAML} providerID={existing?.id} />}
      {!isLDAP && !isSAML && (
        <OAuthFields data={data} setData={setData} isNew={isNew} />
      )}

      <Field label="Default role on first sign-in">
        <Select
          value={data.default_role_id ?? ''}
          onChange={(e) => setData((d) => ({ ...d, default_role_id: e.target.value }))}
        >
          <option value="">— none —</option>
          {rolesList.map((r) => (
            <option key={r.id} value={r.id}>{r.name}</option>
          ))}
        </Select>
      </Field>

      <label style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
        <input
          type="checkbox"
          checked={data.enabled ?? true}
          onChange={(e) => setData((d) => ({ ...d, enabled: e.target.checked }))}
        />
        <span style={{ fontSize: 12, color: 'var(--text-body)' }}>Enabled (show on login page / accept logins)</span>
      </label>
      <ErrorBanner message={err} />
    </Modal>
  );
}

/* ── Sub-form: OAuth2 / OIDC ────────────────────────────── */
function OAuthFields({
  data, setData, isNew,
}: { data: Partial<AdminIDP>; setData: React.Dispatch<React.SetStateAction<Partial<AdminIDP>>>; isNew: boolean }) {
  const set = (patch: Partial<AdminIDP>) => setData((d) => ({ ...d, ...patch }));
  return (
    <>
      <Field label="Client ID">
        <TextInput autoComplete="off" value={data.client_id ?? ''} onChange={(e) => set({ client_id: e.target.value })} />
      </Field>
      <Field label={`Client secret${isNew ? '' : ' (leave blank to keep current)'}`}>
        <TextInput
          type="password"
          autoComplete="new-password"
          value={data.client_secret ?? ''}
          onChange={(e) => set({ client_secret: e.target.value })}
        />
      </Field>
      <Field label="Issuer URL" hint="Optional — used for OIDC discovery">
        <TextInput
          placeholder="https://accounts.google.com"
          value={data.issuer ?? ''}
          onChange={(e) => set({ issuer: e.target.value })}
        />
      </Field>
      <details style={detailsStyle}>
        <summary style={summaryStyle}>Advanced: endpoint overrides &amp; claim mapping</summary>
        <Field label="Authorization URL">
          <TextInput value={data.auth_url ?? ''} onChange={(e) => set({ auth_url: e.target.value })} />
        </Field>
        <Field label="Token URL">
          <TextInput value={data.token_url ?? ''} onChange={(e) => set({ token_url: e.target.value })} />
        </Field>
        <Field label="Userinfo URL">
          <TextInput value={data.userinfo_url ?? ''} onChange={(e) => set({ userinfo_url: e.target.value })} />
        </Field>
        <Field label="Scopes">
          <TextInput value={data.scopes ?? ''} onChange={(e) => set({ scopes: e.target.value })} />
        </Field>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8, marginTop: 8 }}>
          <Field label="Subject claim">
            <TextInput value={data.subject_claim ?? ''} onChange={(e) => set({ subject_claim: e.target.value })} />
          </Field>
          <Field label="Email claim">
            <TextInput value={data.email_claim ?? ''} onChange={(e) => set({ email_claim: e.target.value })} />
          </Field>
          <Field label="Username claim">
            <TextInput value={data.username_claim ?? ''} onChange={(e) => set({ username_claim: e.target.value })} />
          </Field>
        </div>
      </details>
    </>
  );
}

/* ── Sub-form: LDAP ─────────────────────────────────────── */
function LDAPFields({
  ldap, setLDAP, isNew,
}: { ldap: LDAPConfig; setLDAP: (p: Partial<LDAPConfig>) => void; isNew: boolean }) {
  return (
    <>
      <Field label="LDAP URL">
        <TextInput
          placeholder="ldap://dc.example.com:389 or ldaps://…"
          value={ldap.url ?? ''}
          onChange={(e) => setLDAP({ url: e.target.value })}
        />
      </Field>
      <Field label="User base DN">
        <TextInput
          placeholder="OU=Users,DC=example,DC=com"
          value={ldap.user_base_dn ?? ''}
          onChange={(e) => setLDAP({ user_base_dn: e.target.value })}
        />
      </Field>
      <Field label="User filter" hint="Use {username} as the placeholder">
        <TextInput
          placeholder="(sAMAccountName={username})"
          value={ldap.user_filter ?? ''}
          onChange={(e) => setLDAP({ user_filter: e.target.value })}
        />
      </Field>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
        <Field label="Username attribute">
          <TextInput value={ldap.username_attr ?? ''} onChange={(e) => setLDAP({ username_attr: e.target.value })} placeholder="sAMAccountName" />
        </Field>
        <Field label="Email attribute">
          <TextInput value={ldap.email_attr ?? ''} onChange={(e) => setLDAP({ email_attr: e.target.value })} placeholder="mail" />
        </Field>
      </div>
      <Field label="Service bind DN" hint="Optional — used to look up users before binding as them">
        <TextInput
          placeholder="CN=svc-kvm,OU=Service,DC=example,DC=com"
          value={ldap.bind_dn ?? ''}
          onChange={(e) => setLDAP({ bind_dn: e.target.value })}
        />
      </Field>
      <Field label={`Service bind password${isNew ? '' : ' (leave blank to keep current)'}`}>
        <TextInput
          type="password"
          autoComplete="new-password"
          value={ldap.bind_password ?? ''}
          onChange={(e) => setLDAP({ bind_password: e.target.value })}
        />
      </Field>
      <div style={{ marginTop: 10, display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <input type="checkbox" checked={!!ldap.start_tls} onChange={(e) => setLDAP({ start_tls: e.target.checked })} />
          <span style={{ fontSize: 12, color: 'var(--text-body)' }}>StartTLS (for ldap:// URLs)</span>
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={!!ldap.insecure_skip_verify}
            onChange={(e) => setLDAP({ insecure_skip_verify: e.target.checked })}
          />
          <span style={{ fontSize: 12, color: 'var(--text-body)' }}>Skip TLS verification</span>
        </label>
      </div>
    </>
  );
}

/* ── Sub-form: SAML ─────────────────────────────────────── */
function SAMLFields({
  saml, setSAML, providerID,
}: { saml: SAMLConfig; setSAML: (p: Partial<SAMLConfig>) => void; providerID?: string }) {
  const spMetadataURL = `${location.protocol}//${location.host}/api/auth/saml/metadata?provider=${providerID ?? '<after-save>'}`;
  return (
    <>
      <div style={{ ...banner, marginTop: 10 }}>
        <strong style={{ color: 'var(--text-strong)' }}>SP metadata URL</strong> (import this at your IdP):
        <code style={codeBlockStyle}>{spMetadataURL}</code>
      </div>
      <Field label="IdP metadata URL" hint="Easiest — we fetch & refresh on demand">
        <TextInput
          placeholder="https://login.example.com/saml/metadata"
          value={saml.metadata_url ?? ''}
          onChange={(e) => setSAML({ metadata_url: e.target.value })}
        />
      </Field>
      <details style={detailsStyle}>
        <summary style={summaryStyle}>Or configure IdP manually (entity ID + SSO URL + certificate)</summary>
        <Field label="IdP entity ID">
          <TextInput value={saml.idp_entity_id ?? ''} onChange={(e) => setSAML({ idp_entity_id: e.target.value })} />
        </Field>
        <Field label="Single sign-on URL">
          <TextInput
            placeholder="https://login.example.com/saml/sso"
            value={saml.sso_url ?? ''}
            onChange={(e) => setSAML({ sso_url: e.target.value })}
          />
        </Field>
        <Field label="IdP signing certificate (PEM)">
          <TextArea
            rows={5}
            style={{ fontFamily: "'SF Mono', monospace", fontSize: 11 }}
            placeholder={'-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----'}
            value={saml.certificate ?? ''}
            onChange={(e) => setSAML({ certificate: e.target.value })}
          />
        </Field>
      </details>
      <details style={detailsStyle}>
        <summary style={summaryStyle}>Advanced: SP entity ID override &amp; attribute mapping</summary>
        <Field label="SP entity ID" hint="Leave blank to use the metadata URL">
          <TextInput value={saml.sp_entity_id ?? ''} onChange={(e) => setSAML({ sp_entity_id: e.target.value })} />
        </Field>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8, marginTop: 8 }}>
          <Field label="Subject attribute">
            <TextInput value={saml.subject_attr ?? ''} placeholder="NameID" onChange={(e) => setSAML({ subject_attr: e.target.value })} />
          </Field>
          <Field label="Email attribute">
            <TextInput value={saml.email_attr ?? ''} placeholder="email" onChange={(e) => setSAML({ email_attr: e.target.value })} />
          </Field>
          <Field label="Username attribute">
            <TextInput value={saml.username_attr ?? ''} placeholder="preferred_username" onChange={(e) => setSAML({ username_attr: e.target.value })} />
          </Field>
        </div>
      </details>
    </>
  );
}

function Empty({ children, danger = false }: { children: React.ReactNode; danger?: boolean }) {
  return (
    <div style={{ padding: 20, textAlign: 'center', fontSize: 12, color: danger ? '#F85149' : 'var(--text-muted)' }}>
      {children}
    </div>
  );
}

const banner: React.CSSProperties = {
  padding: '10px 12px',
  marginBottom: 14,
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  fontSize: 11,
  color: 'var(--text-muted)',
};
const codeBlockStyle: React.CSSProperties = {
  display: 'block',
  marginTop: 6,
  padding: '6px 8px',
  background: 'var(--surface-2)',
  borderRadius: 3,
  fontFamily: "'SF Mono', monospace",
  fontSize: 11,
  color: 'var(--text-body)',
  userSelect: 'all',
  wordBreak: 'break-all',
};
const detailsStyle: React.CSSProperties = {
  marginTop: 10,
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '8px 12px',
};
const summaryStyle: React.CSSProperties = {
  cursor: 'pointer',
  color: 'var(--text-muted)',
  fontSize: 11,
};
