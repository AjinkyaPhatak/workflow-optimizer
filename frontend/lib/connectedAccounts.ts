import type { ConnectedAccount, ConnectedAccountStatus } from "@/types/api";

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
