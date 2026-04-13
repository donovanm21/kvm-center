// Thin fetch wrapper that:
//   - sends session cookies (same-origin),
//   - surfaces 401 as a typed error the auth layer can catch,
//   - parses JSON once and hands back the narrowed type.
//
// All hooks/queries funnel through apiGet / apiPost so we have exactly
// one place to add cross-cutting concerns (retries, tracing, etc.).

export class UnauthorizedError extends Error {
  constructor() {
    super('not authenticated');
    this.name = 'UnauthorizedError';
  }
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = 'ApiError';
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = {
    method,
    credentials: 'same-origin',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  };
  const r = await fetch(path, init);
  if (r.status === 401) throw new UnauthorizedError();
  if (!r.ok) {
    let msg = r.statusText;
    try {
      const d = await r.json();
      if (d?.error) msg = d.error;
    } catch {
      // swallow — fall back to statusText
    }
    throw new ApiError(r.status, msg);
  }
  // Some endpoints return 204/empty body.
  const text = await r.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const apiGet = <T>(path: string) => request<T>('GET', path);
export const apiPost = <T>(path: string, body?: unknown) => request<T>('POST', path, body);
export const apiPut = <T>(path: string, body?: unknown) => request<T>('PUT', path, body);
export const apiDelete = <T>(path: string) => request<T>('DELETE', path);
