// The single HTTP boundary of the frontend. Components never call fetch():
// they call the resource modules (auth, workflows, ...) built on request().

import type { ApiErrorBody, ValidationError } from "@/types/api";

/** An error answered by the API in its standard error shape. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }

  /** Graph validation findings carried by a 422 VALIDATION_ERROR. */
  get validationErrors(): ValidationError[] {
    const d = this.details as { errors?: ValidationError[] } | null;
    return this.code === "VALIDATION_ERROR" && Array.isArray(d?.errors) ? d.errors : [];
  }
}

export type TokenProvider = () => string | null;

let tokenProvider: TokenProvider = () => null;
let onUnauthenticated: () => void = () => {};

/** Wires the bearer token source and the 401 handler (lib/auth). */
export function configureClient(opts: { token: TokenProvider; onUnauthenticated: () => void }) {
  tokenProvider = opts.token;
  onUnauthenticated = opts.onUnauthenticated;
}

export const API_PREFIX = "/api/v1";

type Query = Record<string, string | number | undefined>;

export interface RequestOptions {
  query?: Query;
  body?: unknown;
  /** Send without the bearer token (login, register). */
  anonymous?: boolean;
  signal?: AbortSignal;
  /** Override for tests. */
  fetchImpl?: typeof fetch;
}

export function buildUrl(path: string, query?: Query): string {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v !== undefined && v !== "") qs.set(k, String(v));
  }
  const s = qs.toString();
  return `${API_PREFIX}${path}${s ? `?${s}` : ""}`;
}

export async function request<T>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  const token = opts.anonymous ? null : tokenProvider();
  if (token) headers.Authorization = `Bearer ${token}`;

  const doFetch = opts.fetchImpl ?? fetch;
  let res: Response;
  try {
    res = await doFetch(buildUrl(path, opts.query), {
      method,
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal,
      cache: "no-store",
    });
  } catch (e) {
    if ((e as Error).name === "AbortError") throw e;
    throw new ApiError(0, "NETWORK_ERROR", "The server could not be reached", null);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let parsed: unknown = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
  }
  if (!res.ok) {
    const body = parsed as Partial<ApiErrorBody> | null;
    const err = new ApiError(
      res.status,
      body?.error?.code ?? `HTTP_${res.status}`,
      body?.error?.message ?? res.statusText ?? "Request failed",
      body?.error?.details ?? null,
    );
    if (res.status === 401 && !opts.anonymous) onUnauthenticated();
    throw err;
  }
  return parsed as T;
}

async function failure(res: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> | null = null;
  try {
    body = (await res.json()) as Partial<ApiErrorBody>;
  } catch {
    body = null;
  }
  if (res.status === 401) onUnauthenticated();
  return new ApiError(res.status, body?.error?.code ?? `HTTP_${res.status}`, body?.error?.message ?? res.statusText ?? "Request failed",
    body?.error?.details ?? null);
}

function authHeaders(): Record<string, string> {
  const token = tokenProvider();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** Authenticated SSE, including split UTF-8 characters and multiline data. */
export async function stream(path: string, onEvent: (kind: string, data: unknown, id: string) => void, signal: AbortSignal): Promise<void> {
  const res = await fetch(buildUrl(path), { headers: { ...authHeaders(), Accept: "text/event-stream" }, cache: "no-store", signal });
  if (!res.ok) throw await failure(res);
  if (!res.body) throw new ApiError(0, "STREAM_UNAVAILABLE", "Live updates are unavailable", null);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value, { stream: !done }).replace(/\r\n/g, "\n");
      let end: number;
      while ((end = buffer.indexOf("\n\n")) !== -1) {
        const block = buffer.slice(0, end); buffer = buffer.slice(end + 2);
        const lines = block.split("\n");
        const kind = lines.find(line => line.startsWith("event:"))?.slice(6).trim() ?? "message";
        const id = lines.find(line => line.startsWith("id:"))?.slice(3).trim() ?? "";
        const data = lines.filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
        if (data) {
          try { onEvent(kind, JSON.parse(data) as unknown, id); } catch { /* Ignore incomplete/malformed event, reconnect from the last accepted ID. */ }
        }
      }
      if (done) break;
    }
  } finally { reader.releaseLock(); }
}

export async function binaryPost(path: string, body: unknown, signal?: AbortSignal): Promise<Blob> {
  const res = await fetch(buildUrl(path), { method: "POST", headers: { ...authHeaders(), "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  if (!res.ok) throw await failure(res);
  return res.blob();
}

/** Posts multipart/form-data (file uploads, Phase D1); the browser sets the
 * boundary. */
export async function upload<T>(path: string, form: FormData, fetchImpl: typeof fetch = fetch): Promise<T> {
  let res: Response;
  try {
    res = await fetchImpl(buildUrl(path), { method: "POST", headers: { Accept: "application/json", ...authHeaders() }, body: form, cache: "no-store" });
  } catch {
    throw new ApiError(0, "NETWORK_ERROR", "The server could not be reached", null);
  }
  if (!res.ok) throw await failure(res);
  return (await res.json()) as T;
}

/** Fetches a binary resource (file downloads) with the session token. */
export async function download(path: string, fetchImpl: typeof fetch = fetch): Promise<Blob> {
  let res: Response;
  try {
    res = await fetchImpl(buildUrl(path), { headers: authHeaders(), cache: "no-store" });
  } catch {
    throw new ApiError(0, "NETWORK_ERROR", "The server could not be reached", null);
  }
  if (!res.ok) throw await failure(res);
  return res.blob();
}
