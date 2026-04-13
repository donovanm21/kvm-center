import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Navigate, Route, Routes } from 'react-router-dom';

import { Layout } from './components/Layout';
import { AuthProvider, useAuth } from './hooks/useAuth';
import { ToastProvider } from './hooks/useToast';
import { subscribeToServerEvents } from './api/events';
import { Login } from './pages/Login';
import { Dashboard } from './pages/Dashboard';
import { VMs } from './pages/VMs';
import { VMDetail } from './pages/VMDetail';
import { Hosts } from './pages/Hosts';
import { HostDetail } from './pages/HostDetail';
import { Storage } from './pages/Storage';
import { SnapshotsOverview } from './pages/SnapshotsOverview';
import { Migrations } from './pages/Migrations';
import { Deployment } from './pages/Deployment';
import { Settings } from './pages/Settings';

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
  const { user, loading } = useAuth();
  const qc = useQueryClient();

  // Subscribe to the server push stream for the lifetime of the
  // authenticated session. Server events invalidate the matching query
  // keys so the UI reflects changes within ~100 ms instead of waiting
  // for the 60 s safety-net poll.
  useEffect(() => {
    if (!user) return;
    return subscribeToServerEvents(qc);
  }, [user, qc]);

  if (loading) {
    return <div className="loading-screen">Loading…</div>;
  }
  if (!user) {
    return <Login />;
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
        <Route path="/snapshots" element={<SnapshotsOverview />} />
        <Route path="/snapshots/:uuid" element={<SnapshotsOverview />} />
        <Route path="/migrations" element={<Migrations />} />
        <Route path="/deployment" element={<Deployment />} />
        <Route path="/settings/*" element={<Settings />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}

