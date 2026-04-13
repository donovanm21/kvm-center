import { useMutation } from '@tanstack/react-query';

import { apiPost } from '@/api/client';

export interface TOTPSetupResponse {
  secret: string;
  otpauth_uri: string;
  issuer: string;
  account: string;
}

export function useTOTPSetup() {
  return useMutation({
    mutationFn: () => apiPost<TOTPSetupResponse>('/api/auth/2fa/setup'),
  });
}

export function useTOTPEnable() {
  return useMutation({
    mutationFn: (code: string) => apiPost<{ enabled: boolean }>('/api/auth/2fa/enable', { code }),
  });
}

export function useTOTPDisable() {
  return useMutation({
    mutationFn: (password: string) => apiPost<{ enabled: boolean }>('/api/auth/2fa/disable', { password }),
  });
}
