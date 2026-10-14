"use client";

// Phase C8: a setting's choices loaded through the node's account (Notion
// databases, Discord channels, sheet columns...). Loading starts once the
// account and every required dependency are set; results are cached per
// workspace, node type, source and settings, so switching nodes does not
// refetch. The request carries setting values only, never secrets.

import { useCallback, useEffect, useState } from "react";
import { ApiError } from "@/lib/api/client";
import { nodeApi } from "@/lib/api";
import type { ConfigOption, NodeDefinition } from "@/types/api";

export interface FieldOptions {
  options: ConfigOption[];
  loading: boolean;
  /** Why the list is not available: a missing setting or a load error. */
  hint: string | null;
  reload: () => void;
}

const cache = new Map<string, ConfigOption[]>();

/** Clears cached choices (tests). */
export function clearFieldOptionsCache() {
  cache.clear();
}

function isSet(v: unknown): boolean {
  return typeof v === "string" ? v.trim() !== "" && !v.includes("{{") : v !== undefined && v !== null;
}

/** What blocks loading, or null when the request can be made. */
export function optionsBlocker(def: NodeDefinition | undefined, dependsOn: string[], config: Record<string, unknown>): string | null {
  if (def?.auth && !isSet(config.credential_id)) return def.auth.credential_type === "OAUTH2" ? "Choose an account to list choices." : "Choose a credential to list choices.";
  for (const dep of dependsOn) {
    const field = def?.config.find((f) => f.name === dep);
    if (field?.required !== false && !isSet(config[dep])) return `Set ${field?.label ?? dep} first.`;
  }
  return null;
}

export function useFieldOptions(nodeType: string, def: NodeDefinition | undefined, source: string | undefined,
  dependsOn: string[], config: Record<string, unknown>, workspaceId: string | null | undefined): FieldOptions {
  const blocker = !source ? "" : !workspaceId ? "Loading…" : optionsBlocker(def, dependsOn, config);
  const sent: Record<string, unknown> = {};
  if (config.credential_id !== undefined) sent.credential_id = config.credential_id;
  for (const dep of dependsOn) if (isSet(config[dep])) sent[dep] = config[dep];
  const key = JSON.stringify([workspaceId, nodeType, source, sent]);
  // Results live in the cache; state only records the last outcome so a
  // finished request re-renders (and remembers its error).
  const [outcome, setOutcome] = useState<{ key: string; error: string | null }>({ key: "", error: null });
  const [nonce, setNonce] = useState(0);
  const ready = blocker === null && !!source && !!workspaceId;
  const failed = outcome.key === key && outcome.error !== null;

  useEffect(() => {
    if (!ready || cache.has(key) || failed) return;
    let cancelled = false;
    nodeApi.options(nodeType, { workspace_id: workspaceId!, source: source!, config: JSON.parse(key)[3] })
      .then((l) => {
        cache.set(key, l.items);
        if (!cancelled) setOutcome({ key, error: null });
      })
      .catch((e) => {
        if (!cancelled) setOutcome({ key, error: e instanceof ApiError ? e.message : "The choices could not be loaded." });
      });
    return () => {
      cancelled = true;
    };
  }, [key, ready, failed, nodeType, source, workspaceId, nonce]);

  const reload = useCallback(() => {
    cache.delete(key);
    setOutcome({ key: "", error: null });
    setNonce((n) => n + 1);
  }, [key]);

  return {
    options: cache.get(key) ?? [],
    loading: ready && !cache.has(key) && !failed,
    hint: blocker || (failed ? outcome.error : null),
    reload,
  };
}
