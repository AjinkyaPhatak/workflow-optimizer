"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { assistantApi } from "@/lib/api/assistant";
import { ApiError } from "@/lib/api/client";
import type { EditorLayout } from "@/types/automation";

export function useEditorLayout(id?: string) {
  const [layout, setLayout] = useState<EditorLayout>({ revision: 0, positions: {} });
  const [loadedId, setLoadedId] = useState(id);
  const latest = useRef({ id, layout });
  useEffect(() => {
    let live = true;
    latest.current = { id, layout: { revision: 0, positions: {} } };
    if (!id) return;
    assistantApi.layout(id).then(value => {
      if (live) { latest.current = { id, layout: value }; setLayout(value); setLoadedId(id); }
    }).catch(() => { /* The canvas remains usable; a save fetches the current revision. */ });
    return () => { live = false; };
  }, [id]);
  const save = useCallback(async (positions: EditorLayout["positions"]) => {
    if (!id) return;
    try {
      const saved = await assistantApi.saveLayout(id, latest.current.id === id ? latest.current.layout.revision : 0, positions);
      if (latest.current.id === id) { latest.current = { id, layout: saved }; setLayout(saved); setLoadedId(id); }
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        const current = await assistantApi.layout(id);
        if (latest.current.id === id) latest.current = { id, layout: current };
      }
      throw err;
    }
  }, [id]);
  return { layout: id === loadedId ? layout : { revision: 0, positions: {} }, save };
}
