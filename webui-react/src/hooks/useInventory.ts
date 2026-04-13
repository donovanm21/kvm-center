import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';
import type { Host, StoragePool, VM, Features } from '@/types';

// SSE is the primary mechanism — the server publishes "inventory" and
// "settings" events that invalidate the matching keys in the React
// Query cache, so the UI updates within a few hundred milliseconds of
// the state change. The interval below is a safety net for the case
// where the EventSource is temporarily disconnected (captive portal,
// browser throttling a background tab, etc.).
const SAFETY_POLL_MS = 60_000;

export const useHosts = () =>
  useQuery({ queryKey: ['hosts'], queryFn: () => apiGet<Host[]>('/api/hosts'), refetchInterval: SAFETY_POLL_MS });

export const useVMs = () =>
  useQuery({ queryKey: ['vms'], queryFn: () => apiGet<VM[]>('/api/vms'), refetchInterval: SAFETY_POLL_MS });

export const useStorage = () =>
  useQuery({ queryKey: ['storage'], queryFn: () => apiGet<StoragePool[]>('/api/storage'), refetchInterval: SAFETY_POLL_MS });

export const useFeatures = () =>
  useQuery({ queryKey: ['features'], queryFn: () => apiGet<Features>('/api/settings'), refetchInterval: SAFETY_POLL_MS });
