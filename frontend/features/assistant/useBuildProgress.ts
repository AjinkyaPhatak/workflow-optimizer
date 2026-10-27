"use client";

import { useEffect, useState } from "react";
import { assistantApi } from "@/lib/api/assistant";
import type { BuildProgress } from "@/types/automation";

export function useBuildProgress(id: string | undefined, revision: number | undefined, building: boolean) {
  const [current, setCurrent] = useState<{ id: string; progress: BuildProgress } | null>(null);
  useEffect(() => {
    if (!id || !building) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const progress = await assistantApi.buildProgress(id, controller.signal);
        if (!controller.signal.aborted) setCurrent({ id, progress });
      } catch { /* Regular request completion remains authoritative after connection loss. */ }
      if (!controller.signal.aborted) timer = setTimeout(() => void poll(), 750);
    };
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [id, building]);
  return building && current !== null && current.id === id && current.progress.revision === revision && current.progress.status === "building" ? current.progress : null;
}
