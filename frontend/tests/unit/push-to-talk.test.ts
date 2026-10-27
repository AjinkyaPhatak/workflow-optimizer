import { afterEach, describe, expect, it, vi } from "vitest";
import { PushToTalk } from "@/features/assistant/pushToTalk";
function setup() {
  const io = { start: vi.fn().mockResolvedValue(undefined), mute: vi.fn(), commit: vi.fn(), close: vi.fn(), changed: vi.fn(), transcript: vi.fn(), recover: vi.fn(), error: vi.fn() };
  return { io, talk: new PushToTalk(io) };
}
afterEach(() => vi.useRealTimers());
describe("push to talk connection lifecycle", () => {
  it("holds through silence, commits once on release, drains one final and closes", () => {
    vi.useFakeTimers(); const { io, talk } = setup();
    talk.press(); talk.ready(); talk.press();
    talk.updatePartial("First sentence");
    vi.advanceTimersByTime(5000);
    talk.committed("unexpected automatic commit");
    expect(io.commit).not.toHaveBeenCalled(); expect(io.transcript).not.toHaveBeenCalled();
    talk.updatePartial("First sentence and second sentence");
    talk.release(); talk.release();
    expect(io.mute).toHaveBeenCalledOnce();
    expect(io.close).not.toHaveBeenCalled();
    vi.advanceTimersByTime(300);
    expect(io.commit).toHaveBeenCalledOnce();
    talk.committed("First sentence and second sentence"); talk.committed("duplicate");
    expect(io.transcript).toHaveBeenCalledExactlyOnceWith("First sentence and second sentence");
    expect(io.close).toHaveBeenCalledOnce();
    expect(talk.phase).toBe("idle");
  });
  it("release during pending token or microphone startup aborts and never commits", async () => {
    const { io, talk } = setup(); let reject!: (error: Error) => void;
    io.start.mockImplementation(() => new Promise<void>((_, fail) => { reject = fail; }));
    talk.press(); const signal = io.start.mock.calls[0][0] as AbortSignal;
    talk.release(); talk.ready(); talk.committed("late"); reject(new Error("late failure"));
    await Promise.resolve();
    expect(signal.aborted).toBe(true); expect(talk.phase).toBe("idle");
    expect(io.commit).not.toHaveBeenCalled(); expect(io.transcript).not.toHaveBeenCalled(); expect(io.error).not.toHaveBeenCalled();
  });
  it("cancellation during hold or drain drops the utterance and clears timers", () => {
    vi.useFakeTimers(); const { io, talk } = setup();
    talk.press(); talk.ready(); talk.updatePartial("cancel me"); talk.release(); talk.cancel();
    vi.runAllTimers(); talk.committed("late final");
    expect(io.commit).not.toHaveBeenCalled(); expect(io.transcript).not.toHaveBeenCalled(); expect(io.recover).not.toHaveBeenCalled();
  });
  it("recovers partial words on failed final delivery and allows a new press", () => {
    vi.useFakeTimers(); const { io, talk } = setup();
    talk.press(); talk.ready(); talk.updatePartial("Keep these words"); talk.release(); vi.advanceTimersByTime(8300);
    expect(io.recover).toHaveBeenCalledExactlyOnceWith("Keep these words"); expect(io.error).toHaveBeenCalledOnce(); expect(io.close).toHaveBeenCalledOnce();
    talk.press(); expect(io.start).toHaveBeenCalledTimes(2); talk.cancel();
  });
  it("unexpected disconnect recovers the partial once; intentional final close does not fail", () => {
    const { io, talk } = setup(); io.close.mockImplementation(() => talk.disconnected());
    talk.press(); talk.ready(); talk.updatePartial("Keep this"); talk.disconnected(); talk.disconnected();
    expect(io.recover).toHaveBeenCalledExactlyOnceWith("Keep this"); expect(io.error).toHaveBeenCalledOnce();
  });
});
