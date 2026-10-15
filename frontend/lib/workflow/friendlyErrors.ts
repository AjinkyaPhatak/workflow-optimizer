// What went wrong, in plain words, and what to do about it (Phase I3).
// Codes are the backend's; the message from the backend is kept as the
// detail when it already says something useful.

export type FixAction = "show_step" | "connect_account" | "add_key" | "try_again" | "fill_inputs";

export interface FriendlyError {
  title: string;
  /** What to do, as a sentence. */
  advice: string;
  action: FixAction;
}

const BY_CODE: Record<string, FriendlyError> = {
  CREDENTIAL_NOT_FOUND: { title: "A step needs an account or key", advice: "Choose one on the step's card, or add it first.", action: "show_step" },
  CREDENTIAL_INVALID: { title: "A step needs an account or key", advice: "Choose one on the step's card.", action: "show_step" },
  CREDENTIAL_PROVIDER_MISMATCH: { title: "A step uses the wrong kind of key", advice: "Pick a key for the service the step uses.", action: "show_step" },
  CREDENTIAL_DECRYPTION_FAILED: { title: "A saved key can no longer be read", advice: "Add the key again on the Credentials page.", action: "add_key" },
  ACCOUNT_RECONNECT_REQUIRED: { title: "An account needs reconnecting", advice: "Reconnect it on the Connected accounts page.", action: "connect_account" },
  AUTHENTICATION_FAILED: { title: "The service did not accept the key or account", advice: "Check the key, or reconnect the account, then run again.", action: "connect_account" },
  PERMISSION_DENIED: { title: "The account is not allowed to do this", advice: "Reconnect the account on the Connected accounts page and allow access.", action: "connect_account" },
  RATE_LIMITED: { title: "The service asked us to slow down", advice: "Free plans allow only a few runs a minute. Wait a minute and run again.", action: "try_again" },
  PROVIDER_UNAVAILABLE: { title: "The service did not answer", advice: "It may be down for a moment. Try again shortly.", action: "try_again" },
  PROVIDER_TIMEOUT: { title: "The service took too long", advice: "Try again; if it keeps happening, ask for less at once.", action: "try_again" },
  PROVIDER_SERVER_ERROR: { title: "The service had a problem", advice: "Try again in a moment.", action: "try_again" },
  NODE_TIMEOUT: { title: "A step took too long", advice: "Try again; if it keeps happening, ask for less at once.", action: "try_again" },
  MODEL_NOT_SUPPORTED: { title: "That AI model is not available", advice: "Pick another model on the step (More settings).", action: "show_step" },
  INVALID_PROVIDER_REQUEST: { title: "The service did not accept what the step sent", advice: "Check the step's settings.", action: "show_step" },
  CONFIGURATION_ERROR: { title: "A step's settings need fixing", advice: "Open the step and fill in what is missing.", action: "show_step" },
  INVALID_INPUT: { title: "A step got something it cannot use", advice: "Check what is connected to it and the answers you gave.", action: "show_step" },
  NOT_FOUND: { title: "Something a step looks for does not exist", advice: "Check the step's settings (a file, sheet, page or channel may be gone).", action: "show_step" },
  UNRESOLVED_REFERENCE: { title: "A blank was never filled in", advice: "Answer every question when you run, or connect something to each {{blank}}.", action: "fill_inputs" },
};

const FALLBACK: FriendlyError = { title: "A step did not work", advice: "Open the step to see what it needs.", action: "show_step" };

/** The plain explanation of an error code. */
export function friendlyError(code: string | undefined, message?: string): FriendlyError & { detail?: string } {
  const known = code ? BY_CODE[code] : undefined;
  const base = known ?? (message && /unresolved|variable reference/i.test(message) ? BY_CODE.UNRESOLVED_REFERENCE : FALLBACK);
  // The backend's message, when it is a plain sentence worth showing.
  const detail = message && message.length < 240 && !/^[A-Z_]+$/.test(message) ? message : undefined;
  return { ...base, detail };
}
