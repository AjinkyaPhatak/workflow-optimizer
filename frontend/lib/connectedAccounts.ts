import type { ConnectedAccount, ConnectedAccountStatus, OAuthProvider } from "@/types/api";

/** Setup metadata is public; OAuth secrets are configured only on the server. */
export const ACCOUNT_PROVIDER_SETUP = [
  { id: "google", name: "Google", description: "Gmail, Google Sheets, Calendar, Docs, Forms and Meet.", prefix: "GOOGLE" },
  { id: "notion", name: "Notion", description: "Search pages and read or update your workspace's databases.", prefix: "NOTION" },
  { id: "airtable", name: "Airtable", description: "Read and update records in your bases.", prefix: "AIRTABLE" },
  { id: "discord", name: "Discord", description: "Read and send messages in your connected server.", prefix: "DISCORD" },
];

/** Keep supported apps and stored accounts visible when server configuration is missing. */
export function accountProviderCards(configured: OAuthProvider[], accounts: ConnectedAccount[]): OAuthProvider[] {
  const cards = new Map<string, OAuthProvider>(ACCOUNT_PROVIDER_SETUP.map(p => [p.id, { ...p, scopes: [] }]));
  for (const account of accounts) {
    if (!cards.has(account.provider)) cards.set(account.provider, {
      id: account.provider, name: account.provider_name || account.provider, description: "", scopes: [],
    });
  }
  for (const provider of configured) cards.set(provider.id, provider);
  return [...cards.values()];
}

/** "Google — ada@example.com" */
export function accountLabel(a: Pick<ConnectedAccount, "provider_name" | "email" | "display_name">): string {
  return `${a.provider_name} — ${a.email || a.display_name || "account"}`;
}

export function statusLabel(s: ConnectedAccountStatus): string {
  switch (s) {
    case "ACTIVE": return "Connected";
    case "EXPIRED": return "Expired: reconnect";
    case "REVOKED": return "Access revoked: reconnect";
    case "ERROR": return "Error: reconnect";
    case "DISCONNECTED": return "Disconnected";
  }
}

/** The outcome the OAuth callback redirected with (?connected= / ?error=). */
export function callbackOutcome(params: URLSearchParams): { kind: "ok" | "error"; text: string } | null {
  if (params.get("connected")) return { kind: "ok", text: "Account connected." };
  const error = params.get("error");
  if (!error) return null;
  const messages: Record<string, string> = {
    invalid_state: "The connection request expired or was already used. Please try again.",
    authorization_denied: "Access was not granted.",
    invalid_code: "The provider did not complete the connection. Please try again.",
    token_exchange_failed: "The provider did not complete the connection. Please try again.",
    provider_unavailable: "The provider is unavailable right now. Please try again later.",
    invalid_configuration: "This provider is not configured on the server.",
  };
  return { kind: "error", text: messages[error] ?? "The account could not be connected." };
}
