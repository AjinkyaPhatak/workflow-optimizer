import type { KnowledgeBase, KnowledgeBaseDetail, KnowledgeDocument, KnowledgeResult, List } from "@/types/api";
import { request } from "./client";

// Knowledge bases (Phase D2). Documents are indexed by the server in the
// background: adding one answers at once with status INDEXING.
export const knowledgeApi = {
  copyLocal: (id: string) => request<KnowledgeBase>("POST", "/knowledge-bases/" + encodeURIComponent(id) + "/local-copy"),
  addURL: (id: string, url: string) => request<KnowledgeDocument>("POST", "/knowledge-bases/" + encodeURIComponent(id) + "/documents", { body: { url } }),
  list: (workspaceId: string) => request<List<KnowledgeBase>>("GET", "/knowledge-bases", { query: { workspace_id: workspaceId } }),
  get: (id: string) => request<KnowledgeBaseDetail>("GET", `/knowledge-bases/${encodeURIComponent(id)}`),
  create: (body: { workspace_id: string; name: string; description: string; credential_id: string }) =>
    request<KnowledgeBase>("POST", "/knowledge-bases", { body }),
  remove: (id: string) => request<void>("DELETE", `/knowledge-bases/${encodeURIComponent(id)}`),
  addText: (id: string, title: string, text: string) =>
    request<KnowledgeDocument>("POST", `/knowledge-bases/${encodeURIComponent(id)}/documents`, { body: { title, text } }),
  addFile: (id: string, fileId: string) =>
    request<KnowledgeDocument>("POST", `/knowledge-bases/${encodeURIComponent(id)}/documents`, { body: { file_id: fileId } }),
  removeDocument: (id: string, docId: string) =>
    request<void>("DELETE", `/knowledge-bases/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`),
  reindex: (id: string, docId: string) =>
    request<KnowledgeDocument>("POST", `/knowledge-bases/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}/reindex`),
  search: (id: string, query: string, topK = 5) =>
    request<List<KnowledgeResult>>("POST", `/knowledge-bases/${encodeURIComponent(id)}/search`, { body: { query, top_k: topK } }),
};
