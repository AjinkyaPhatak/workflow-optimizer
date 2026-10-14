import type { List, WorkspaceFile } from "@/types/api";
import { download, request, upload } from "./client";

// Workspace files (Phase D1). Uploads are multipart; downloads are fetched
// with the session token and saved by the browser.
export const fileApi = {
  list: (workspaceId: string) => request<List<WorkspaceFile>>("GET", "/files", { query: { workspace_id: workspaceId } }),
  upload: (workspaceId: string, file: File) => {
    const form = new FormData();
    form.set("workspace_id", workspaceId);
    form.set("file", file, file.name);
    return upload<WorkspaceFile>("/files", form);
  },
  content: (id: string) => download(`/files/${encodeURIComponent(id)}/content`),
  remove: (id: string) => request<void>("DELETE", `/files/${encodeURIComponent(id)}`),
};
