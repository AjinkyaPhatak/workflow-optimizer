import type { AuthorizationStarted, ConnectedAccount, List, OAuthProvider } from "@/types/api";
import { request } from "./client";

// Connected accounts (Phase C2). Metadata only: the OAuth exchange happens
// between the provider and the backend, and tokens never reach the browser.
export const connectedAccountApi = {
  providers: () => request<List<OAuthProvider>>("GET", "/oauth/providers"),
  list: (workspaceId: string) => request<List<ConnectedAccount>>("GET", "/connected-accounts", { query: { workspace_id: workspaceId } }),
  /** Starts a connection; the browser is then sent to authorization_url. */
  authorize: (provider: string, workspaceId: string) =>
    request<AuthorizationStarted>("POST", `/connected-accounts/${encodeURIComponent(provider)}/authorize`, { body: { workspace_id: workspaceId } }),
  disconnect: (id: string) => request<ConnectedAccount>("DELETE", `/connected-accounts/${encodeURIComponent(id)}`),
};
