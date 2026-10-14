import type { ConfigOption, List, NodeDefinition } from "@/types/api";
import { request } from "./client";

export const nodeApi = {
  /** The backend node registry: the only source of node types, ports and config fields. */
  list: () => request<List<NodeDefinition>>("GET", "/nodes"),
  /** Phase C8: a setting's choices, listed through the node's account. */
  options: (nodeType: string, body: { workspace_id: string; source: string; config: Record<string, unknown> }) =>
    request<List<ConfigOption>>("POST", `/nodes/${encodeURIComponent(nodeType)}/options`, { body }),
};
