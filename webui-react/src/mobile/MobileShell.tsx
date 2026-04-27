import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom';

import { BrandLogo } from '@/components/Brand';
import { useAuth } from '@/hooks/useAuth';

// MobileShell — top header (brand + logout) and bottom tab bar
// (Dashboard / Hosts / VMs / Activity). Body sits between them and
// is the only scrollable region, so the tab bar always stays
// reachable on long lists. iOS safe-area insets respected via
// env(safe-area-inset-*).
export function MobileShell() {
  const { user, logout } = useAuth();
  const nav = useNavigate();

  async function onLogout() {
    await logout();
    // Send the user back to the login screen via the mobile route
    // so the next sign-in lands them straight on /m.
    nav('/m', { replace: true });
  }

  return (
    <div className="m-shell" data-testid="mobile-shell">
      <header className="m-header">
        <Link to="/m" className="m-header-brand">
          <BrandLogo size={22} />
          <span className="m-header-title">
            <span className="m-header-primary">kvm</span>
            <span className="m-header-accent">center</span>
          </span>
        </Link>
        <div className="m-header-actions">
          <button
            type="button"
            className="m-icon-btn"
            onClick={onLogout}
            title={`Sign out ${user?.username ?? ''}`}
            aria-label="Sign out"
          >⏏</button>
        </div>
      </header>

      <main className="m-body">
        <Outlet />
      </main>

      <nav className="m-tabbar" aria-label="Primary navigation">
        <TabLink to="/m" end glyph="◉" label="Overview" />
        <TabLink to="/m/hosts" glyph="▤" label="Hosts" />
        <TabLink to="/m/vms" glyph="▣" label="VMs" />
        <TabLink to="/m/activity" glyph="≡" label="Activity" />
      </nav>
    </div>
  );
}

function TabLink({ to, end, glyph, label }: { to: string; end?: boolean; glyph: string; label: string }) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) => 'm-tab' + (isActive ? ' is-active' : '')}
    >
      <span className="m-tab-glyph" aria-hidden>{glyph}</span>
      <span className="m-tab-label">{label}</span>
    </NavLink>
  );
}
