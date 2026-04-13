import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Navigate, Route, Routes } from 'react-router-dom';

import { Layout } from './components/Layout';
import { AuthProvider, useAuth } from './hooks/useAuth';
import { subscribeToServerEvents } from './api/events';
import { Login } from './pages/Login';
import { Dashboard } from './pages/Dashboard';

// Port of each v3 view lands as its own route and replaces the stub.
// Phase 1 ships Dashboard parity only.
export function App() {
  return (
    <AuthProvider>
      <Shell />
    </AuthProvider>
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
        <Route path="/vms" element={<ComingSoon title="Virtual Machines" />} />
        <Route path="/storage" element={<ComingSoon title="Storage" />} />
        <Route path="/settings" element={<ComingSoon title="Settings" />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}

function ComingSoon({ title }: { title: string }) {
  return (
    <div className="panel">
      <h2>{title}</h2>
      <p style={{ fontSize: 12, color: 'var(--text-muted)', margin: 0 }}>
        This view hasn't been ported to the v4 React shell yet — use the{' '}
        <a href="/" style={{ color: 'var(--accent)' }}>v3 UI</a> for now. Ports land one view at a time.
      </p>
    </div>
  );
}
