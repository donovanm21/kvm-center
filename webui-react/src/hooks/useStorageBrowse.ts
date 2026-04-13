import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiDelete, apiGet } from '@/api/client';
import { useToast } from './useToast';
import type { StorageBrowseResponse } from '@/types';

/**
 * Directory listing for a given pool path on a host. The server proxies
 * the request to the agent's /storage-browse handler — that's where the
 * actual fs walk happens, inside the hypervisor.
 */
export function useStorageBrowse(hostID: string, path: string) {
  return useQuery({
    queryKey: ['storage-browse', hostID, path],
    queryFn: () =>
      apiGet<StorageBrowseResponse>(
        `/api/storage/browse?host=${encodeURIComponent(hostID)}&path=${encodeURIComponent(path)}`,
      ),
    enabled: Boolean(hostID && path),
  });
}

/**
 * File upload uses a raw fetch (not our json-only apiPost) because the
 * body is the file's binary stream and the server passes through to the
 * agent as-is. Progress events aren't wired yet — Phase-2 work.
 */
export function useUploadFile(hostID: string, path: string) {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: async (file: File) => {
      const r = await fetch(
        `/api/storage/upload?host=${encodeURIComponent(hostID)}&path=${encodeURIComponent(path)}`,
        {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': file.type || 'application/octet-stream' },
          body: file,
        },
      );
      if (!r.ok) {
        const text = await r.text();
        throw new Error(text || r.statusText);
      }
      return r.json().catch(() => ({}));
    },
    onSuccess: (_d, file) => {
      toast('success', `Uploaded ${file.name}`);
      qc.invalidateQueries({ queryKey: ['storage-browse', hostID, path] });
      qc.invalidateQueries({ queryKey: ['storage'] });
    },
    onError: (err) => {
      toast('error', 'Upload failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}

export function useDeleteFile(hostID: string, path: string) {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (filePath: string) =>
      apiDelete(
        `/api/storage/file?host=${encodeURIComponent(hostID)}&path=${encodeURIComponent(filePath)}`,
      ),
    onSuccess: (_d, filePath) => {
      const name = filePath.split('/').pop() || filePath;
      toast('success', `Deleted ${name}`);
      qc.invalidateQueries({ queryKey: ['storage-browse', hostID, path] });
      qc.invalidateQueries({ queryKey: ['storage'] });
    },
    onError: (err) => {
      toast('error', 'Delete failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}
