export type TalkPhase = "idle" | "starting" | "listening" | "draining";

/** One press owns one connection and one manual commit. Async startup cannot outlive it. */
export class PushToTalk {
  phase: TalkPhase = "idle";
  private request: AbortController | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private partial = "";
  private commitRequested = false;
  constructor(private readonly io: {
    start: (signal: AbortSignal) => Promise<void>;
    mute: () => void;
    commit: () => void;
    close: () => void;
    changed: (phase: TalkPhase) => void;
    transcript: (text: string) => void;
    recover: (text: string) => void;
    error: (error: unknown) => void;
  }) {}
  private change(phase: TalkPhase) { this.phase = phase; this.io.changed(phase); }
  private finish() {
    clearTimeout(this.timer);
    this.request?.abort(); this.request = null;
    this.change("idle"); this.partial = ""; this.commitRequested = false;
    this.io.close();
  }
  press() {
    if (this.phase !== "idle") return;
    const request = new AbortController(); this.request = request;
    this.partial = ""; this.change("starting");
    this.timer = setTimeout(() => this.fail(new Error("The voice connection timed out. Try again or continue by typing.")), 20_000);
    void this.io.start(request.signal).catch(error => {
      if (!request.signal.aborted) this.fail(error);
    });
  }
  ready() {
    if (this.phase !== "starting") return;
    clearTimeout(this.timer); this.change("listening");
  }
  release() {
    if (this.phase === "starting") { this.cancel(); return; }
    if (this.phase !== "listening") return;
    this.change("draining");
    try {
      this.io.mute();
      // The installed SDK buffers 4096 samples at 16 kHz (256 ms) and has no
      // flush API. Muting immediately, then letting silence fill that chunk,
      // includes the end of the utterance before requesting the final.
      this.timer = setTimeout(() => {
        this.commitRequested = true;
        this.timer = setTimeout(() => this.fail(new Error("The final transcript did not arrive. Review your words below, or try voice again.")), 8_000);
        try { this.io.commit(); } catch (error) { this.fail(error); }
      }, 300);
    } catch (error) { this.fail(error); }
  }
  updatePartial(text: string) { if (this.phase === "listening" || this.phase === "draining") this.partial = text; }
  committed(text: string) {
    if (this.phase !== "draining" || !this.commitRequested) return;
    this.finish();
    if (text.trim()) this.io.transcript(text);
  }
  fail(error: unknown) {
    if (this.phase === "idle") return;
    const partial = this.partial;
    this.finish();
    if (partial.trim()) this.io.recover(partial);
    this.io.error(error);
  }
  disconnected() {
    if (this.phase !== "idle") this.fail(new Error("The voice connection ended. You can try again or continue by typing."));
  }
  cancel() { if (this.phase !== "idle") this.finish(); }
}
