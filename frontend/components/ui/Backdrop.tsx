import type { ReactNode } from "react";
import { NodeIcon } from "./NodeIcon";

/**
 * Decorations behind a page header: a dot grid, outlined rings, a few flat
 * shapes in the theme colours, and little app cards joined by a flowing
 * wire. Purely decorative.
 */
export function Backdrop({ apps = ["gmail", "google_sheets", "google_calendar", "google_docs"], icons }: {
  apps?: string[];
  /** Four icons to float instead of app logos (first flow, lone card, second flow). */
  icons?: ReactNode[];
}) {
  const ic = icons ?? [
    <NodeIcon key="0" integrationId={apps[0]} size={18} />, <NodeIcon key="1" integrationId={apps[1]} size={16} />,
    <NodeIcon key="2" integrationId={apps[2]} size={18} />, <NodeIcon key="3" integrationId={apps[3]} size={18} />,
  ];
  return (
    <div className="backdrop" aria-hidden>
      <span className="bd-dots" />
      <svg className="bd-rings left" viewBox="0 0 200 200"><circle cx="100" cy="100" r="96" /><circle cx="100" cy="100" r="70" /><circle cx="100" cy="100" r="44" /></svg>
      <svg className="bd-rings right" viewBox="0 0 200 200"><circle cx="100" cy="100" r="96" /><circle cx="100" cy="100" r="70" /></svg>
      <span className="bd-shape circle" /><span className="bd-shape square" /><span className="bd-shape half" /><span className="bd-shape ring" />
      <span className="bd-shape pill" /><span className="bd-shape dot" />
      <div className="bd-flow left">
        <span className="bd-app">{ic[0]}</span>
        <svg viewBox="0 0 120 70"><path d="M2 10 C 60 10, 60 60, 118 60" /></svg>
        <span className="bd-app"><NodeIcon def={{ type: "llm", category: "ai" }} size={18} /></span>
      </div>
      <div className="bd-flow right">
        <span className="bd-app">{ic[2]}</span>
        <svg viewBox="0 0 120 70"><path d="M2 60 C 60 60, 60 10, 118 10" /></svg>
        <span className="bd-app">{ic[3]}</span>
      </div>
      <span className="bd-app lone a">{ic[1]}</span>
      <span className="bd-spark s1">✳</span><span className="bd-spark s2">✳</span><span className="bd-spark s3">✳</span>
    </div>
  );
}

/** Flat colour plus a pattern and one bold shape, in place of gradients. */
export const MOTIFS = ["motif-teal", "motif-charcoal", "motif-sun", "motif-peach", "motif-sandy"] as const;

/** A stable motif for an ID, so each card keeps its look. */
export function motifOf(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return MOTIFS[h % MOTIFS.length];
}
