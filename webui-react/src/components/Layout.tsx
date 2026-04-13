import { useEffect, useState, type ReactNode } from 'react';
import { NavLink, useLocation } from 'react-router-dom';

import { apiGet } from '@/api/client';
import { BrandLogo } from './Brand';
import { UserMenu } from './UserMenu';
import { useAuth } from '@/hooks/useAuth';
import { useTheme } from '@/hooks/useTheme';

interface NavEntry {
  to: string;
  label: string;
  icon: ReactNode;
  /** permission key; entry is hidden when the user lacks it */
  requires?: string;
}

const NAV_MAIN: NavEntry[] = [
  { to: '/', label: 'Dashboard', icon: <HomeIcon /> },
  { to: '/vms', label: 'Virtual Machines', icon: <VMIcon />, requires: 'vm.view' },
  { to: '/hosts', label: 'Hosts', icon: <HostIcon />, requires: 'host.view' },
  { to: '/storage', label: 'Storage', icon: <StorageIcon />, requires: 'storage.view' },
];

const NAV_ADMIN: NavEntry[] = [
  { to: '/settings', label: 'Settings', icon: <GearIcon />, requires: 'user.manage' },
];

export function Layout({ children }: { children: ReactNode }) {
  const { hasPerm } = useAuth();
  const showAdmin = NAV_ADMIN.some((e) => !e.requires || hasPerm(e.requires));

  return (
    <div className="app-shell">
      <Topbar />
      <div className="app-body">
        <nav className="sidebar">
          <div className="sidebar-section-title">Overview</div>
          {NAV_MAIN.filter((e) => !e.requires || hasPerm(e.requires)).map((e) => (
            <NavLink
              key={e.to}
              to={e.to}
              end={e.to === '/'}
              className={({ isActive }) => 'nav-item' + (isActive ? ' active' : '')}
            >
              {e.icon}
              <span>{e.label}</span>
            </NavLink>
          ))}
          {showAdmin && (
            <>
              <div className="sidebar-section-title">Administration</div>
              {NAV_ADMIN.filter((e) => !e.requires || hasPerm(e.requires)).map((e) => (
                <NavLink
                  key={e.to}
                  to={e.to}
                  className={({ isActive }) => 'nav-item' + (isActive ? ' active' : '')}
                >
                  {e.icon}
                  <span>{e.label}</span>
                </NavLink>
              ))}
            </>
          )}
        </nav>
        <main className="main">
          <Breadcrumb />
          {children}
        </main>
      </div>
    </div>
  );
}

function Topbar() {
  const [version, setVersion] = useState<string>('…');
  const [theme, toggleTheme] = useTheme();
  useEffect(() => {
    apiGet<{ version: string }>('/api/version')
      .then((d) => setVersion(d.version || 'dev'))
      .catch(() => setVersion('dev'));
  }, []);
  return (
    <header className="topbar">
      <div className="logo">
        <BrandLogo size={40} />
        <div className="brand-text">
          <span className="brand">
            <span className="primary">kvm</span>
            <span className="accent">center</span>
          </span>
          <span className="brand-sub">KVM management, simplified</span>
        </div>
      </div>
      <div className="spacer" />
      <span className="version-badge">{version}</span>
      <button
        type="button"
        onClick={toggleTheme}
        aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
        title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
        style={iconBtnStyle}
      >
        {theme === 'dark' ? <MoonIcon /> : <SunIcon />}
      </button>
      <UserMenu />
    </header>
  );
}

function MoonIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor">
      <path d="M6 .278a.77.77 0 0 1 .08.858 7.2 7.2 0 0 0-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.78.78 0 0 1 .81.316.73.73 0 0 1-.031.893A8.35 8.35 0 0 1 8.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.75.75 0 0 1 6 .278z" />
    </svg>
  );
}
function SunIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor">
      <path d="M8 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM8 0a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 0zm0 13a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 13zm8-5a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2a.5.5 0 0 1 .5.5zM3 8a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2A.5.5 0 0 1 3 8zm10.657-5.657a.5.5 0 0 1 0 .707l-1.414 1.415a.5.5 0 1 1-.707-.708l1.414-1.414a.5.5 0 0 1 .707 0zm-9.193 9.193a.5.5 0 0 1 0 .707L3.05 13.657a.5.5 0 0 1-.707-.707l1.414-1.414a.5.5 0 0 1 .707 0zm9.193 2.121a.5.5 0 0 1-.707 0l-1.414-1.414a.5.5 0 0 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .707zM4.464 4.465a.5.5 0 0 1-.707 0L2.343 3.05a.5.5 0 1 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .708z" />
    </svg>
  );
}

const iconBtnStyle: React.CSSProperties = {
  width: 32,
  height: 32,
  background: 'transparent',
  color: 'var(--text-muted)',
  border: '1px solid var(--border)',
  borderRadius: 5,
  cursor: 'pointer',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
};

function Breadcrumb() {
  const loc = useLocation();
  const parts = loc.pathname === '/' ? ['Dashboard'] : loc.pathname.split('/').filter(Boolean);
  return (
    <nav className="breadcrumb" aria-label="breadcrumb">
      KVM Center{' '}
      {parts.map((p, i) => (
        <span key={i}>
          {' / '}
          <strong>{p.charAt(0).toUpperCase() + p.slice(1)}</strong>
        </span>
      ))}
    </nav>
  );
}

/* ── Nav icons (inline so no web-font dependency) ───────── */
function HostIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
      <path d="M14 1H2A1.5 1.5 0 0 0 .5 2.5v3A1.5 1.5 0 0 0 2 7h12a1.5 1.5 0 0 0 1.5-1.5v-3A1.5 1.5 0 0 0 14 1zM2 2h12a.5.5 0 0 1 .5.5v3a.5.5 0 0 1-.5.5H2a.5.5 0 0 1-.5-.5v-3A.5.5 0 0 1 2 2zm1 1.5a1 1 0 1 0 2 0 1 1 0 0 0-2 0zM14 9H2a1.5 1.5 0 0 0-1.5 1.5v3A1.5 1.5 0 0 0 2 15h12a1.5 1.5 0 0 0 1.5-1.5v-3A1.5 1.5 0 0 0 14 9zM2 10h12a.5.5 0 0 1 .5.5v3a.5.5 0 0 1-.5.5H2a.5.5 0 0 1-.5-.5v-3A.5.5 0 0 1 2 10zm1 1.5a1 1 0 1 0 2 0 1 1 0 0 0-2 0z" />
    </svg>
  );
}
function HomeIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
      <path d="M6.5 14v-4h3v4h3V8H15L8 1 1 8h2.5v6z" />
    </svg>
  );
}
function VMIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
      <path d="M1 3.5A1.5 1.5 0 0 1 2.5 2h11A1.5 1.5 0 0 1 15 3.5v7a1.5 1.5 0 0 1-1.5 1.5H10v1h1.5a.5.5 0 0 1 0 1h-7a.5.5 0 0 1 0-1H6v-1H2.5A1.5 1.5 0 0 1 1 10.5z" />
    </svg>
  );
}
function StorageIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
      <path d="M4.318 2.687C5.234 2.271 6.536 2 8 2s2.766.27 3.682.687C12.644 3.125 13 3.627 13 4c0 .374-.356.875-1.318 1.313C10.766 5.729 9.464 6 8 6s-2.766-.27-3.682-.687C3.356 4.875 3 4.373 3 4c0-.374.356-.875 1.318-1.313M13 5.698V7c0 .374-.356.875-1.318 1.313C10.766 8.729 9.464 9 8 9s-2.766-.27-3.682-.687C3.356 7.875 3 7.373 3 7V5.698c.271.202.58.378.904.525C4.978 6.711 6.427 7 8 7s3.022-.289 4.096-.777A5 5 0 0 0 13 5.698m0 3V10c0 .374-.356.875-1.318 1.313C10.766 11.729 9.464 12 8 12s-2.766-.27-3.682-.687C3.356 10.875 3 10.373 3 10V8.698c.271.202.58.378.904.525C4.978 9.711 6.427 10 8 10s3.022-.289 4.096-.777A5 5 0 0 0 13 8.698m0 3V13c0 .374-.356.875-1.318 1.313C10.766 14.729 9.464 15 8 15s-2.766-.27-3.682-.687C3.356 13.875 3 13.373 3 13v-1.302c.271.202.58.378.904.525C4.978 12.711 6.427 13 8 13s3.022-.289 4.096-.777q.325-.147.904-.525" />
    </svg>
  );
}
function GearIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
      <path d="M8 4.754a3.246 3.246 0 1 0 0 6.492 3.246 3.246 0 0 0 0-6.492M5.754 8a2.246 2.246 0 1 1 4.492 0 2.246 2.246 0 0 1-4.492 0" />
      <path d="M9.796 1.343c-.527-1.79-3.065-1.79-3.592 0l-.094.319a.873.873 0 0 1-1.255.52l-.292-.16c-1.64-.892-3.433.902-2.54 2.541l.159.292a.873.873 0 0 1-.52 1.255l-.319.094c-1.79.527-1.79 3.065 0 3.592l.319.094a.873.873 0 0 1 .52 1.255l-.16.292c-.892 1.64.901 3.434 2.541 2.54l.292-.159a.873.873 0 0 1 1.255.52l.094.319c.527 1.79 3.065 1.79 3.592 0l.094-.319a.873.873 0 0 1 1.255-.52l.292.16c1.64.893 3.434-.902 2.54-2.541l-.159-.292a.873.873 0 0 1 .52-1.255l.319-.094c1.79-.527 1.79-3.065 0-3.592l-.319-.094a.873.873 0 0 1-.52-1.255l.16-.292c.893-1.64-.902-3.433-2.541-2.54l-.292.159a.873.873 0 0 1-1.255-.52zm-2.633.283c.246-.835 1.428-.835 1.674 0l.094.319a1.873 1.873 0 0 0 2.693 1.115l.291-.16c.764-.415 1.6.42 1.184 1.185l-.159.292a1.873 1.873 0 0 0 1.116 2.692l.318.094c.835.246.835 1.428 0 1.674l-.319.094a1.873 1.873 0 0 0-1.115 2.693l.16.291c.415.764-.42 1.6-1.185 1.184l-.291-.159a1.873 1.873 0 0 0-2.693 1.116l-.094.318c-.246.835-1.428.835-1.674 0l-.094-.319a1.873 1.873 0 0 0-2.692-1.115l-.292.16c-.764.415-1.6-.42-1.184-1.185l.159-.291A1.873 1.873 0 0 0 1.945 8.93l-.319-.094c-.835-.246-.835-1.428 0-1.674l.319-.094A1.873 1.873 0 0 0 3.06 4.377l-.16-.292c-.415-.764.42-1.6 1.185-1.184l.292.159a1.873 1.873 0 0 0 2.692-1.115z" />
    </svg>
  );
}
