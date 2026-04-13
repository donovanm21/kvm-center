import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPost } from '@/api/client';
import { useToast } from './useToast';

export interface VMCreateInfo {
  os_variants: string[];
  bridges: string[];
  pools: { name: string; path: string }[];
  isos: string[];
}

export interface VMCreatePayload {
  name: string;
  memory_mb: number;
  vcpus: number;
  disk_size_gb: number;
  disk_format: 'qcow2' | 'raw';
  os_variant?: string;
  iso_path?: string;
  import_disk?: string;
  bridge: string;
  pool_path: string;
  firmware: 'bios' | 'efi';
  autostart: boolean;
}

interface CreateResult {
  success?: boolean;
  error?: string;
  message?: string;
  output?: string;
}

/** Host-scoped capabilities for the wizard dropdowns. */
export function useVMCreateInfo(hostID: string | null) {
  return useQuery({
    queryKey: ['vm-create-info', hostID],
    queryFn: () =>
      apiGet<VMCreateInfo>(`/api/vm-create-info?host=${encodeURIComponent(hostID!)}`),
    enabled: Boolean(hostID),
    // Agent shells out to virt-install/virsh to build this list; don't
    // hammer the box while the user flips between hosts.
    staleTime: 60_000,
  });
}

export function useCreateVM(hostID: string | null) {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (payload: VMCreatePayload) =>
      apiPost<CreateResult>(
        `/api/vm-create?host=${encodeURIComponent(hostID!)}`,
        payload,
      ),
    onSuccess: (data, payload) => {
      if (data && data.success === false) {
        toast('error', 'Create failed: ' + (data.error || 'unknown error'));
        return;
      }
      toast('success', `VM "${payload.name}" created`);
      qc.invalidateQueries({ queryKey: ['vms'] });
      qc.invalidateQueries({ queryKey: ['hosts'] });
    },
    onError: (err) => {
      toast('error', 'Create failed: ' + (err instanceof Error ? err.message : String(err)));
    },
  });
}
