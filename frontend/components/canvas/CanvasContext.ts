"use client";

import { createContext, useContext } from "react";
import type { Issue } from "@/lib/workflow/issues";

/** The port a connection is being dragged from. */
export interface ConnectingFrom {
  nodeId: string;
  port: string;
  /** "source": dragging from an output; "target": from an input. */
  handleType: "source" | "target";
}

/** Canvas-only presentation state shared with the node renderers. */
export interface CanvasState {
  issues: Issue[];
  connecting: ConnectingFrom | null;
}

export const CanvasContext = createContext<CanvasState>({ issues: [], connecting: null });

export const useCanvasState = () => useContext(CanvasContext);
