import type { Credential, CreateCredentialBody, List } from "@/types/api";
import { request } from "./client";

// Credentials (Phases 11, C7). Listing returns metadata only. A secret is
// sent once, in the create request, and is never returned or kept in
// browser state afterwards.
export const credentialApi = {
  list: (workspaceId: string) => request<List<Credential>>("GET", "/credentials", { query: { workspace_id: workspaceId } }),
  create: (body: CreateCredentialBody) => request<Credential>("POST", "/credentials", { body }),
  remove: (id: string) => request<void>("DELETE", `/credentials/${encodeURIComponent(id)}`),
};
