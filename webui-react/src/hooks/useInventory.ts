import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';
import type { Host, StoragePool, VM, Features } from '@/types';

// The polling intervals here are a Phase-1 bridge until the SSE push
// stream lands — once the server invalidates these keys on state change
// the hooks flip to { refetchOnMount: false, staleTime: Infinity } and
// the cache becomes authoritative.
const POLL_MS = 15_000;

export const useHosts = () =>
  useQuery({ queryKey: ['hosts'], queryFn: () => apiGet<Host[]>('/api/hosts'), refetchInterval: POLL_MS });

export const useVMs = () =>
  useQuery({ queryKey: ['vms'], queryFn: () => apiGet<VM[]>('/api/vms'), refetchInterval: POLL_MS });

export const useStorage = () =>
  useQuery({ queryKey: ['storage'], queryFn: () => apiGet<StoragePool[]>('/api/storage'), refetchInterval: POLL_MS });

export const useFeatures = () =>
  useQuery({ queryKey: ['features'], queryFn: () => apiGet<Features>('/api/settings') });
