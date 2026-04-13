import React from 'react';
import ReactDOM from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';

import { App } from './App';
import './styles/globals.css';

// Single query client for the whole app. Stale time matches the coarse
// polling cadence of v3 — once the SSE push layer lands this drops to
// Infinity for most queries (cache stays fresh because the server
// invalidates explicitly).
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/v4">
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
