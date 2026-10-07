import { ApiError } from "@/lib/api/client";

/** A safe message for any error (API errors carry the backend's message). */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return "Something went wrong";
}

export function ErrorBanner({ error }: { error: unknown }) {
  if (!error) return null;
  return <div className="error-banner" role="alert">{errorMessage(error)}</div>;
}
