// Query state is separate from inventory content: [] is confirmed empty data,
// while undefined means no successful response has arrived yet.
interface InventoryQueryState {
  data: unknown;
  isError: boolean;
  isFetching: boolean;
}

export function InventoryStatus({ label, query }: { label: string; query: InventoryQueryState }) {
  const message = query.data === undefined
    ? (query.isError ? `${label} unavailable. Retry by refreshing the page.` : `Loading ${label}…`)
    : query.isError
      ? `${label}: showing last known data; refresh failed.`
      : query.isFetching
        ? `${label}: refreshing; showing last known data.`
        : null;
  return message ? <div role="status" style={{ color: 'var(--text-muted)', fontSize: 12 }}>{message}</div> : null;
}
