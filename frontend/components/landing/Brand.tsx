import { APPS, type AppKey } from "./data";

const GLYPHS = {
  input: "M3 12h11M10 8l4 4-4 4M15 4h4a1 1 0 011 1v14a1 1 0 01-1 1h-4",
  output: "M10 12h11M17 8l4 4-4 4M9 4H5a1 1 0 00-1 1v14a1 1 0 001 1h4",
  mic: "M12 3a3 3 0 013 3v6a3 3 0 01-6 0V6a3 3 0 013-3zM5 11a7 7 0 0014 0M12 18v3",
};

/** An app logo in its brand colour (or white on dark), or a line glyph. */
export function AppLogo({ app, size = 22, mono }: { app: AppKey | keyof typeof GLYPHS; size?: number; mono?: string }) {
  if (app in GLYPHS) {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden fill="none" stroke={mono ?? "currentColor"} strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d={GLYPHS[app as keyof typeof GLYPHS]} />
      </svg>
    );
  }
  const b = APPS[app as AppKey];
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path d={b.path} fill={mono ?? `#${b.hex}`} />
    </svg>
  );
}
