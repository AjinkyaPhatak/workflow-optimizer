import type { List, NodeDefinition } from "@/types/api";
import { request } from "./client";

export const nodeApi = {
  /** The backend node registry: the only source of node types, ports and config fields. */
  list: () => request<List<NodeDefinition>>("GET", "/nodes"),
};
