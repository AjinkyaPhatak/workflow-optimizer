import type { Credential, List } from "@/types/api";
import { request } from "./client";

// Credential metadata only. The frontend never creates or reads secrets here:
// secrets are write-only on the API and are never kept in browser state.
export const credentialApi = {
  list: (workspaceId: string) => request<List<Credential>>("GET", "/credentials", { query: { workspace_id: workspaceId } }),
};
