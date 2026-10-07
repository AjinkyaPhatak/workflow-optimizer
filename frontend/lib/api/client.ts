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
