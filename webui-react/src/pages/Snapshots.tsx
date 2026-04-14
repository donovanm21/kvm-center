import { NavLink, Route, Routes } from 'react-router-dom';

import { SnapshotsOverview } from './SnapshotsOverview';
import { SnapshotPolicies } from './SnapshotPolicies';

/**
 * Snapshots top-level page. Two tabs: Inventory (the existing two-pane
 * "VMs with snapshots" view) and Policies (the v5.3 scheduler config).
 * Routes:
 *   /snapshots                → inventory
 *   /snapshots/policies       → policies
 *   /snapshots/vm/:uuid       → inventory with a VM selected
 *
 * The /vm/ prefix on the per-VM route disambiguates from the literal
 * "policies" segment so React Router doesn't try to render a VM whose
 * UUID is "policies".
 */
const TABS = [
  { to: '',         label: 'Inventory', end: true },
  { to: 'policies', label: 'Policies',  end: false },
] as const;

export function Snapshots() {
  return (
    <>
      <div style={tabsStyle}>
        {TABS.map((t) => (
          <NavLink
            key={t.to || 'index'}
            to={t.to ? `/snapshots/${t.to}` : '/snapshots'}
            end={t.end}
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
        <Route index            element={<SnapshotsOverview />} />
        <Route path="policies"  element={<SnapshotPolicies />} />
        <Route path="vm/:uuid"  element={<SnapshotsOverview />} />
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
