"use client";

// The OAuth providers set up on the server (GET /oauth/providers), loaded
// once per page. Apps whose accounts come from a provider that is not set up
// are shown as "Coming soon" and cannot be used.

import { useEffect, useState } from "react";
import { connectedAccountApi } from "@/lib/api";

let cached: Promise<Set<string>> | null = null;

function load(): Promise<Set<string>> {
  cached ??= connectedAccountApi.providers()
    .then((l) => new Set(l.items.map((p) => p.id)))
    .catch((e) => {
      cached = null;
      throw e;
    });
  return cached;
}

/** The IDs of the providers set up on the server; null while loading (or
 * when they could not be loaded: then nothing is marked Coming soon). */
export function useProviders(): Set<string> | null {
  const [providers, setProviders] = useState<Set<string> | null>(null);
  useEffect(() => {
    let cancelled = false;
    load().then((p) => !cancelled && setProviders(p)).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);
  return providers;
}

/** Test hook: forget the loaded providers. */
export function resetProviders() {
  cached = null;
}
