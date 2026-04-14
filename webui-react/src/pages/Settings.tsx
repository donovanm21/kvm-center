import { NavLink, Navigate, Route, Routes } from 'react-router-dom';

import { GeneralTab } from './settings/GeneralTab';
import { UsersTab } from './settings/UsersTab';
import { GroupsTab } from './settings/GroupsTab';
import { RolesTab } from './settings/RolesTab';
import { ProvidersTab } from './settings/ProvidersTab';
import { TLSTab } from './settings/TLSTab';
import { DatabaseTab } from './settings/DatabaseTab';
import { AuditTab } from './settings/AuditTab';
import { NotificationsTab } from './settings/NotificationsTab';
import { TokensTab } from './settings/TokensTab';
import { MetricsTab } from './settings/MetricsTab';

const TABS = [
  { to: 'general', label: 'General' },
  { to: 'users', label: 'Users' },
  { to: 'groups', label: 'Groups' },
  { to: 'roles', label: 'Roles' },
  { to: 'providers', label: 'Identity Providers' },
  { to: 'tls', label: 'TLS' },
  { to: 'database', label: 'Database' },
  { to: 'audit', label: 'Audit Log' },
  { to: 'notifications', label: 'Notifications' },
  { to: 'tokens', label: 'API Tokens' },
  { to: 'metrics', label: 'Metrics' },
] as const;

export function Settings() {
  return (
    <>
      <h2 style={{ margin: '0 0 16px 0', fontSize: 18, fontWeight: 600, color: 'var(--text-strong)' }}>
        Settings
      </h2>

      <div style={tabsStyle}>
        {TABS.map((t) => (
          <NavLink
            key={t.to}
            to={`/settings/${t.to}`}
            style={({ isActive }) => ({
              ...tabStyle,
              color: isActive ? 'var(--text-strong)' : 'var(--text-muted)',
              borderBottomColor: isActive ? 'var(--accent)' : 'transparent',
            })}
          >
            {t.label}
          </NavLink>
        ))}
      </div>

      <Routes>
        <Route index element={<Navigate to="general" replace />} />
        <Route path="general" element={<GeneralTab />} />
        <Route path="users" element={<UsersTab />} />
        <Route path="groups" element={<GroupsTab />} />
        <Route path="roles" element={<RolesTab />} />
        <Route path="providers" element={<ProvidersTab />} />
        <Route path="tls" element={<TLSTab />} />
        <Route path="database" element={<DatabaseTab />} />
        <Route path="audit" element={<AuditTab />} />
        <Route path="notifications" element={<NotificationsTab />} />
        <Route path="tokens" element={<TokensTab />} />
        <Route path="metrics" element={<MetricsTab />} />
      </Routes>
    </>
  );
}

const tabsStyle: React.CSSProperties = {
  display: 'flex',
  gap: 4,
  borderBottom: '1px solid var(--border)',
  marginBottom: 16,
};
const tabStyle: React.CSSProperties = {
  padding: '8px 16px',
  fontSize: 13,
  fontWeight: 500,
  textDecoration: 'none',
  borderBottom: '2px solid transparent',
};
