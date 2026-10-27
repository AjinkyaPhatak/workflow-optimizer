import { ApiError } from "@/lib/api/client";

export function voiceErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "Your session ended. Sign in again to use voice.";
    if (error.status === 503 && error.code === "SERVICE_UNAVAILABLE") return "Voice is unavailable on the server. You can continue by typing.";
    return error.message + " You can continue by typing.";
  }
  const name = error instanceof Error ? error.name : "";
  const message = error instanceof Error ? error.message : "";
  if (name === "NotAllowedError" || /permission denied|permission dismissed|notallowederror/i.test(message))
    return "Microphone access is blocked. Allow it in your browser’s site settings, then try again. You can still type below.";
  if (name === "NotFoundError" || /requested device not found|notfounderror/i.test(message))
    return "No microphone was found. Connect a microphone or continue by typing.";
  if (name === "NotReadableError" || /could not start audio source|notreadableerror/i.test(message))
    return "Your microphone is busy or unavailable. Close other apps using it and try again, or type below.";
  return "The voice connection failed. Try the microphone again or continue by typing.";
}
