import { Navigate, Route, Routes } from 'react-router-dom';

import { MobileShell } from './MobileShell';
import { MobileDashboard } from './screens/MobileDashboard';
import { MobileHosts } from './screens/MobileHosts';
import { MobileHostDetail } from './screens/MobileHostDetail';
import { MobileVMs } from './screens/MobileVMs';
import { MobileVMDetail } from './screens/MobileVMDetail';
import { MobileActivity } from './screens/MobileActivity';
import './mobile.css';

// v5.46.0: Mobile route tree. A parallel `/m/*` namespace with its
// own shell + bottom-tab nav. Read-only by design; full operator
// actions stay on the desktop SPA. Reuses the same AuthProvider,
// API client, query keys, and React Query cache.
export function MobileApp() {
  return (
    <Routes>
      <Route path="/m" element={<MobileShell />}>
        <Route index element={<MobileDashboard />} />
        <Route path="hosts" element={<MobileHosts />} />
        <Route path="hosts/:id" element={<MobileHostDetail />} />
        <Route path="vms" element={<MobileVMs />} />
        <Route path="vms/:uuid" element={<MobileVMDetail />} />
        <Route path="activity" element={<MobileActivity />} />
      </Route>
      <Route path="/m/*" element={<Navigate to="/m" replace />} />
    </Routes>
  );
}
