import type { List, Project } from "@/types/api";
import { request } from "./client";

export const projectApi = {
  list: (workspaceId: string) => request<List<Project>>("GET", "/projects", { query: { workspace_id: workspaceId } }),
  get: (id: string) => request<Project>("GET", `/projects/${id}`),
  create: (workspaceId: string, name: string) =>
    request<Project>("POST", "/projects", { body: { workspace_id: workspaceId, name } }),
};
