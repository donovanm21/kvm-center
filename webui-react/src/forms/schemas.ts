import { z } from 'zod';

// Reusable Zod primitives (v5.41.0). Centralised so a tweak to,
// e.g., what counts as a valid VM name lands in one place rather
// than five modals. Zod-validated forms unblock RHF's typed
// `register`/`watch` and let inline error surfaces use one
// rendering pattern.

/**
 * VM name — libvirt accepts a generous range; we hold to a
 * reasonable subset that doesn't surprise filesystem tooling
 * (qcow2 paths get mangled by spaces and quotes).
 */
export const vmNameSchema = z
  .string()
  .min(1, 'VM name is required')
  .max(64, 'VM name must be 64 characters or fewer')
  .regex(/^[A-Za-z0-9._-]+$/, 'Use letters, digits, dot, underscore, hyphen only');

/** Hypervisor / hostname-style identifier — mirrors libvirt + DNS rules. */
export const hostnameSchema = z
  .string()
  .min(1, 'Hostname is required')
  .max(253, 'Hostname must be 253 characters or fewer')
  .regex(
    /^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/,
    'Use a valid DNS hostname (letters, digits, dot, hyphen)',
  );

/** Snapshot name — same shape as VM names; the libvirt snapshot
 * scheduler fails noisily otherwise. */
export const snapshotNameSchema = vmNameSchema;

/** Memory in MB — clamped to a humane range. */
export const memMBSchema = z
  .number()
  .int('Memory must be a whole number of MB')
  .min(128, 'Memory must be at least 128 MB')
  .max(4 * 1024 * 1024, 'Memory must be 4 TB or less');

/** vCPU count — libvirt's ceiling is per-host, but 1-128 covers
 * everything the homelab targets. */
export const vcpuSchema = z
  .number()
  .int('vCPUs must be a whole number')
  .min(1, 'At least one vCPU is required')
  .max(128, '128 vCPUs is the upper bound');

/** Disk size in GB. */
export const diskGBSchema = z
  .number()
  .int('Disk size must be a whole number of GB')
  .min(1, 'Disk must be at least 1 GB')
  .max(64 * 1024, 'Disk must be 64 TB or less');

/** Bearer token shape — kvmc_<8 char id>.<40 char secret>. The
 * secret half is opaque base32-ish. */
export const bearerTokenSchema = z
  .string()
  .regex(/^kvmc_[A-Za-z0-9]{8}\.[A-Za-z0-9]{40}$/, 'Tokens are kvmc_<id>.<secret>');

/** URL with explicit https requirement (or http on localhost for dev). */
export const httpsUrlSchema = z
  .string()
  .url('Must be a valid URL')
  .refine(
    (s) => s.startsWith('https://') || s.startsWith('http://localhost') || s.startsWith('http://127.'),
    'Use https:// (http allowed only on localhost/127.x)',
  );
