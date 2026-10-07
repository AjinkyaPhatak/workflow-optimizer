// Polling for the execution debugger (no WebSockets/SSE in Phase 14): load
// now, then again every `intervalMs` while the snapshot says the execution
// is still active. Events are appended just after each state change, so
// after a terminal status the poller settles a few more times until the
// terminal event has arrived.

export interface PollerOptions<T> {
  load: () => Promise<T>;
  /** True when no further polling is needed. */
  isDone: (snapshot: T) => boolean;
  /** True once the snapshot is terminal (settling starts). */
  isTerminal?: (snapshot: T) => boolean;
  onData: (snapshot: T) => void;
  onError?: (err: unknown) => void;
  intervalMs?: number;
  settleMs?: number;
  maxSettles?: number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

export interface Poller {
  stop: () => void;
  /** Whether a further poll is scheduled. */
  active: () => boolean;
}

export const DEFAULT_POLL_MS = 2000;

export function startPolling<T>(o: PollerOptions<T>): Poller {
  const interval = o.intervalMs ?? DEFAULT_POLL_MS;
  const settleMs = o.settleMs ?? 500;
  const maxSettles = o.maxSettles ?? 3;
  const setTimer = o.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = o.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  let stopped = false;
  let handle: unknown = null;
  let settles = 0;

  const tick = async () => {
    handle = null;
    if (stopped) return;
    try {
      const snap = await o.load();
      if (stopped) return;
      o.onData(snap);
      if (o.isDone(snap)) {
        stopped = true;
        return;
      }
      if (o.isTerminal?.(snap)) {
        if (++settles > maxSettles) {
          stopped = true;
          return;
        }
        handle = setTimer(tick, settleMs);
        return;
      }
    } catch (e) {
      if (stopped) return;
      o.onError?.(e);
    }
    handle = setTimer(tick, interval);
  };

  void tick();
  return {
    stop: () => {
      stopped = true;
      if (handle !== null) clearTimer(handle);
      handle = null;
    },
    active: () => !stopped,
  };
}
