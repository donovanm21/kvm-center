import { useEffect } from 'react';
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
import { VMDetail } from './pages/VMDetail';
import { Hosts } from './pages/Hosts';
import { HostDetail } from './pages/HostDetail';
import { Storage } from './pages/Storage';
import { Snapshots } from './pages/Snapshots';
import { KVMTools } from './pages/KVMTools';
import { Migrations } from './pages/Migrations';
import { Deployment } from './pages/Deployment';
import { Settings } from './pages/Settings';
import { SystemHealth } from './pages/SystemHealth';
import { Docs } from './pages/Docs';
import { qk } from '@/queryKeys';

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

function Shell() {
  const { user, loading, hasPerm } = useAuth();
  const qc = useQueryClient();

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
  const location = useLocation();
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
    </Layout>
  );
}

