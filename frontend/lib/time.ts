// Times in plain words (Phase I2).

/** "just now", "5 min ago", "3 h ago", "2 days ago", else the date. */
export function timeAgo(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.round(h / 24);
  if (d < 7) return `${d} day${d === 1 ? "" : "s"} ago`;
  return new Date(t).toLocaleDateString();
}

/** "0.4 s", "12 s", "3 min 5 s". */
export function tookLabel(ms: number): string {
  if (ms < 10_000) return `${Math.round(ms / 100) / 10} s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  return `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ""}`;
}
