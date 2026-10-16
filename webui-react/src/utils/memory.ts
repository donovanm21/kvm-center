// Inventory memory fields are MiB. Keep both sides of a ratio in the same
// binary unit; a positive sub-GiB capacity must never become "0 GB".
export function formatMemoryUsage(usedMiB: number | null | undefined, totalMiB: number | null | undefined): string {
  if (typeof totalMiB !== 'number' || !Number.isFinite(totalMiB) || totalMiB <= 0) return '—';
  const useGiB = totalMiB >= 1024;
  const format = (value: number) => useGiB ? (value / 1024).toFixed(1) : String(value);
  const used = typeof usedMiB === 'number' && Number.isFinite(usedMiB) && usedMiB >= 0 ? format(usedMiB) : '—';
  return `${used} / ${format(totalMiB)} ${useGiB ? 'GiB' : 'MiB'}`;
}
