"use client";

import { createContext, useContext } from "react";
import type { EditorSession } from "./useEditorSession";

/** Server state shared by the editor's components (catalog, credentials,
 * workflow, version, server actions). */
export const EditorContext = createContext<EditorSession | null>(null);

export function useEditorContext(): EditorSession {
  const ctx = useContext(EditorContext);
  if (!ctx) throw new Error("useEditorContext outside EditorContext");
  return ctx;
}
