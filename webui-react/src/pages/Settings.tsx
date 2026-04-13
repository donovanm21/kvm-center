import { NavLink, Navigate, Route, Routes } from 'react-router-dom';

import { GeneralTab } from './settings/GeneralTab';
import { UsersTab } from './settings/UsersTab';

const TABS = [
  { to: 'general', label: 'General' },
  { to: 'users', label: 'Users' },
  { to: 'groups', label: 'Groups' },
  { to: 'roles', label: 'Roles' },
  { to: 'providers', label: 'Identity Providers' },
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
        <Route path="groups" element={<SettingsStub label="Groups" />} />
        <Route path="roles" element={<SettingsStub label="Roles" />} />
        <Route path="providers" element={<SettingsStub label="Identity Providers" />} />
      </Routes>
    </>
  );
}

function SettingsStub({ label }: { label: string }) {
  return (
    <div className="panel">
      <h2>{label}</h2>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: 0 }}>
        Port of this sub-tab lands in the next v4 commit — use the{' '}
        <a href="/" style={{ color: 'var(--accent)' }}>v3 UI</a> for now.
      </p>
    </div>
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
