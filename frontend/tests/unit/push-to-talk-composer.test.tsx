// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AssistantHome } from "@/components/assistant/AssistantHome";
const mocks = vi.hoisted(() => ({
  state: { workspaceId: "workspace", workspaces: [{ id: "workspace", role: "owner" }], sessions: [], credentials: [], events: [], session: null, run: null, building: false, send: vi.fn().mockResolvedValue(true) },
  scribe: { status: "disconnected", isConnected: false, isTranscribing: false, partialTranscript: "", connect: vi.fn().mockResolvedValue(undefined), disconnect: vi.fn(), mute: vi.fn(), commit: vi.fn(), clearTranscripts: vi.fn() },
  token: vi.fn().mockResolvedValue({ token: "ephemeral" }),
  callbacks: {} as { onCommittedTranscript: (value: { text: string }) => void },
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/features/assistant/useAssistant", () => ({ useAssistant: () => mocks.state }));
vi.mock("@/features/assistant/useBuildProgress", () => ({ useBuildProgress: () => null }));
vi.mock("@/features/assistant/useEditorLayout", () => ({ useEditorLayout: () => ({}) }));
vi.mock("@/components/assistant/WorkflowDraft", () => ({ WorkflowDraft: () => null }));
vi.mock("@/components/assistant/WorkflowManagement", () => ({ WorkflowManagement: () => null }));
vi.mock("@/lib/api/assistant", () => ({ assistantApi: { voiceToken: mocks.token, workflows: vi.fn().mockResolvedValue({ items: [] }) } }));
vi.mock("@elevenlabs/react", () => ({ CommitStrategy: { MANUAL: "manual" }, useScribe: (options: typeof mocks.callbacks) => { mocks.callbacks = options; return mocks.scribe; } }));
beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks(); mocks.scribe.isConnected = false; mocks.state.building = false;
  Object.defineProperty(window, "isSecureContext", { value: true, configurable: true });
  Object.defineProperty(navigator, "mediaDevices", { value: { getUserMedia: vi.fn() }, configurable: true });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });
it("keyboard hold ignores repeats, commits on release, and closes after the final", async () => {
  const view = render(<AssistantHome />);
  const mic = screen.getByRole("button", { name: "Hold to speak" });
  fireEvent.keyDown(mic, { key: " ", repeat: false });
  fireEvent.keyDown(mic, { key: " ", repeat: true });
  await act(async () => {});
  expect(mocks.token).toHaveBeenCalledOnce();
  mocks.scribe.isConnected = true; view.rerender(<AssistantHome />);
  expect(screen.getByRole("button", { name: "Release to send" })).toBeTruthy();
  fireEvent.keyUp(mic, { key: " " });
  await act(async () => { vi.advanceTimersByTime(300); });
  expect(mocks.scribe.mute).toHaveBeenCalledOnce(); expect(mocks.scribe.commit).toHaveBeenCalledOnce();
  expect(mocks.scribe.disconnect).not.toHaveBeenCalled();
  await act(async () => { mocks.callbacks.onCommittedTranscript({ text: "Create a report" }); });
  expect(mocks.scribe.disconnect).toHaveBeenCalledOnce(); expect(mocks.state.send).toHaveBeenCalledWith("Create a report");
});
it("keeps a released utterance pending when focus moves to the typed composer", async () => {
  const view = render(<AssistantHome />);
  const mic = screen.getByRole("button", { name: "Hold to speak" });
  fireEvent.keyDown(mic, { key: " " }); await act(async () => {});
  mocks.scribe.isConnected = true; view.rerender(<AssistantHome />);
  fireEvent.keyUp(mic, { key: " " }); fireEvent.blur(mic);
  await act(async () => { vi.advanceTimersByTime(300); });
  expect(mocks.scribe.commit).toHaveBeenCalledOnce();
  await act(async () => { mocks.callbacks.onCommittedTranscript({ text: "Send the results" }); });
  expect(mocks.state.send).toHaveBeenCalledWith("Send the results");
});
it.each(["escape", "blur", "unmount"])("%s cancels a keyboard hold without committing or sending", async reason => {
  const view = render(<AssistantHome />); const mic = screen.getByRole("button", { name: "Hold to speak" });
  fireEvent.keyDown(mic, { key: "Enter" }); await act(async () => {});
  mocks.scribe.isConnected = true; view.rerender(<AssistantHome />);
  if (reason === "escape") fireEvent.keyDown(window, { key: "Escape" });
  if (reason === "blur") fireEvent.blur(mic);
  if (reason === "unmount") view.unmount();
  await act(async () => { vi.advanceTimersByTime(1000); mocks.callbacks.onCommittedTranscript({ text: "Do not send" }); });
  expect(mocks.scribe.disconnect).toHaveBeenCalledOnce(); expect(mocks.scribe.commit).not.toHaveBeenCalled(); expect(mocks.state.send).not.toHaveBeenCalled();
});
it("builds disable the microphone while the typed composer stays editable", () => {
  mocks.state.building = true; render(<AssistantHome />);
  expect(screen.getByRole("button", { name: "Hold to speak" }).matches(":disabled")).toBe(true);
  const composer = screen.getByLabelText("What would you like to do?") as HTMLTextAreaElement;
  fireEvent.change(composer, { target: { value: "Keep my next idea" } });
  expect(composer.value).toBe("Keep my next idea"); expect(composer.disabled).toBe(false);
});
it.each(["mouse", "touch", "pen"])("captures a %s pointer and commits even when released outside", async pointerType => {
  const view = render(<AssistantHome />); const mic = screen.getByRole("button", { name: "Hold to speak" });
  const capture = vi.fn(); const release = vi.fn();
  Object.assign(mic, { setPointerCapture: capture, hasPointerCapture: () => true, releasePointerCapture: release });
  const pointer = (type: string) => Object.assign(new MouseEvent(type, { bubbles: true, button: 0 }), { pointerId: 7, pointerType });
  fireEvent(mic, pointer("pointerdown")); await act(async () => {});
  expect(capture).toHaveBeenCalledWith(7);
  mocks.scribe.isConnected = true; view.rerender(<AssistantHome />);
  // Pointer capture delivers an outside release back to the held button.
  fireEvent(mic, pointer("pointerup"));
  await act(async () => { vi.advanceTimersByTime(300); });
  expect(release).toHaveBeenCalledWith(7); expect(mocks.scribe.commit).toHaveBeenCalledOnce();
});
