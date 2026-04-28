import { Suspense, lazy, useEffect } from 'react';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom';

import { apiGet } from './api/client';

import { Layout } from './components/Layout';
import { AuthProvider, useAuth } from './hooks/useAuth';
import { ToastProvider } from './hooks/useToast';
import { subscribeToServerEvents } from './api/events';
import { Login } from './pages/Login';
import { Setup } from './pages/Setup';
import { Dashboard } from './pages/Dashboard';
import { VMs } from './pages/VMs';
import { Hosts } from './pages/Hosts';
import { Storage } from './pages/Storage';
import { Snapshots } from './pages/Snapshots';
import { KVMTools } from './pages/KVMTools';
import { Migrations } from './pages/Migrations';
import { MobileApp } from './mobile/MobileApp';
import { qk } from '@/queryKeys';

// v5.47.0 bundle code-split: heavy/admin-only routes ride their own
// chunk so first-paint on the dashboard doesn't pay for the audit
// table, settings forms, system-health gauges, doc renderer, etc.
const VMDetail = lazy(() => import('./pages/VMDetail').then((m) => ({ default: m.VMDetail })));
const HostDetail = lazy(() => import('./pages/HostDetail').then((m) => ({ default: m.HostDetail })));
const Deployment = lazy(() => import('./pages/Deployment').then((m) => ({ default: m.Deployment })));
const Settings = lazy(() => import('./pages/Settings').then((m) => ({ default: m.Settings })));
const SystemHealth = lazy(() => import('./pages/SystemHealth').then((m) => ({ default: m.SystemHealth })));
const Docs = lazy(() => import('./pages/Docs').then((m) => ({ default: m.Docs })));

// Port of each v3 view lands as its own route and replaces the stub.
// Phase 1 ships Dashboard parity only.
export function App() {
  return (
    <ToastProvider>
      <AuthProvider>
        <Shell />
      </AuthProvider>
    </ToastProvider>
  );
}

// Auto-redirect mobile visitors from `/` to `/m`. Mobile is the
// canonical experience on iPhone — no opt-out via UI. Power users
// can still set `localStorage.kvmc-prefer-desktop = '1'` via dev
// tools if they need the desktop SPA on a phone-class device.
function useMobileAutoRedirect() {
  const location = useLocation();
  const nav = useNavigate();
  useEffect(() => {
    if (location.pathname.startsWith('/m')) return;
    if (location.pathname !== '/') return;
    try {
      if (localStorage.getItem('kvmc-prefer-desktop') === '1') return;
    } catch { /* private mode — fall through */ }
    const ua = navigator.userAgent || '';
    const isPhone = /iPhone|iPod|Android.*Mobile/.test(ua) || window.innerWidth <= 640;
    if (isPhone) nav('/m', { replace: true });
  }, [location.pathname, nav]);
}

function Shell() {
  const { user, loading, hasPerm } = useAuth();
  const qc = useQueryClient();
  const location = useLocation();
  useMobileAutoRedirect();

  // The /m route tree owns its own shell + nav. Render it unwrapped
  // by the desktop Layout so we get the bottom-tab UI all the way
  // down. Auth-gating still happens here — MobileApp falls through
  // to the same <Login /> when there's no user, so deep-link to
  // /m/vms after login works.
  const isMobileRoute = location.pathname === '/m' || location.pathname.startsWith('/m/');

  // Subscribe to the server push stream for the lifetime of the
  // authenticated session. Server events invalidate the matching query
  // keys so the UI reflects changes within ~100 ms instead of waiting
  // for the 60 s safety-net poll.
  useEffect(() => {
    if (!user) return;
    return subscribeToServerEvents(qc);
  }, [user, qc]);

  // First-boot wizard gate. Runs after login so it has user context.
  // Only admins see the wizard; non-admins skip it entirely because
  // completing it requires user.manage anyway.
  const setup = useQuery({
    queryKey: qk.setupState(),
    queryFn: () => apiGet<{ complete: boolean }>('/api/setup/state'),
    enabled: !!user,
  });
  const nav = useNavigate();
  const isAdmin = hasPerm('user.manage') || hasPerm('admin');
  useEffect(() => {
    if (!user || !setup.data || setup.data.complete) return;
    if (!isAdmin) return;
    if (location.pathname.startsWith('/setup')) return;
    nav('/setup', { replace: true });
  }, [user, setup.data, isAdmin, location.pathname, nav]);

  if (loading) {
    return <div className="loading-screen">Loading…</div>;
  }
  if (!user) {
    return <Login />;
  }
  if (isMobileRoute) {
    return <MobileApp />;
  }
  if (setup.data && !setup.data.complete && isAdmin) {
    return (
      <Routes>
        <Route path="/setup" element={<Setup />} />
        <Route path="*" element={<Navigate to="/setup" replace />} />
      </Routes>
    );
  }
  return (
    <Layout>
      <Suspense fallback={<div className="loading-screen" style={{ padding: 24 }}>Loading…</div>}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/vms" element={<VMs />} />
          <Route path="/vms/:uuid" element={<VMDetail />} />
          <Route path="/vms/:uuid/:tab" element={<VMDetail />} />
          <Route path="/hosts" element={<Hosts />} />
          <Route path="/hosts/:id" element={<HostDetail />} />
          <Route path="/hosts/:id/:tab" element={<HostDetail />} />
          <Route path="/storage" element={<Storage />} />
          <Route path="/snapshots/*" element={<Snapshots />} />
          <Route path="/migrations" element={<Migrations />} />
          <Route path="/kvm-tools" element={<KVMTools />} />
          <Route path="/deployment" element={<Deployment />} />
          <Route path="/system-health" element={<SystemHealth />} />
          <Route path="/settings/*" element={<Settings />} />
          <Route path="/docs" element={<Docs />} />
          <Route path="/docs/:slug" element={<Docs />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
    </Layout>
  );
}

