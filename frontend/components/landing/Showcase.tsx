"use client";

import { useEffect, useRef, useState } from "react";
import { AppLogo } from "./Brand";
import { SCENARIOS, type Scenario } from "./data";

/** Types text out whenever it changes. */
function useTyped(text: string) {
  const [typed, setTyped] = useState({ text, n: 0 });
  useEffect(() => {
    let i = 0;
    const id = window.setInterval(() => {
      i++;
      setTyped({ text, n: i });
      if (i >= text.length) clearInterval(id);
    }, 28);
    return () => clearInterval(id);
  }, [text]);
  return typed.text === text ? text.slice(0, typed.n) : "";
}

/** One workflow drawn as cards joined by flowing wires. */
function FlowCanvas({ s, active }: { s: Scenario; active: boolean }) {
  // Node anchor points in the 100x100 viewBox (cards are ~22 wide, ~16 tall).
  const out = (i: number) => ({ x: s.nodes[i].x + 22, y: s.nodes[i].y + 8 });
  const inp = (i: number) => ({ x: s.nodes[i].x, y: s.nodes[i].y + 8 });
  return (
    <div className={`lp-canvas${active ? " is-active" : ""}`}>
      <div className="lp-canvas-dots" aria-hidden />
      <svg className="lp-wires" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden>
        {s.wires.map(([a, b], i) => {
          const p = out(a), q = inp(b), mid = (p.x + q.x) / 2;
          const d = `M${p.x},${p.y} C${mid},${p.y} ${mid},${q.y} ${q.x},${q.y}`;
          return (
            <g key={i}>
              <path d={d} className="lp-wire-base" vectorEffect="non-scaling-stroke" />
              <path d={d} className="lp-wire-flow" vectorEffect="non-scaling-stroke" style={{ animationDelay: `${i * 0.35}s` }} />
            </g>
          );
        })}
      </svg>
      {s.nodes.map((n, i) => (
        <div key={i} className="lp-node" style={{ left: `${n.x}%`, top: `${n.y}%`, transitionDelay: `${i * 0.12}s` }}>
          <span className="lp-node-tag">{n.tag}</span>
          <span className="lp-node-body">
            <span className="lp-node-icon">
              <AppLogo app={n.app} size={18} mono={n.app === "output" || n.app === "input" || n.app === "mic" ? "#fff" : undefined} />
            </span>
            {n.label}
          </span>
        </div>
      ))}
      <span className="lp-canvas-run" aria-hidden><span className="lp-dot" /> Run complete</span>
    </div>
  );
}

/**
 * The sticky showcase: the left panel stays put while workflows scroll by on
 * the right; the panel's colour, result and typed request follow whichever
 * workflow is in the middle of the screen.
 */
export function Showcase() {
  const [active, setActive] = useState(0);
  const items = useRef<(HTMLDivElement | null)[]>([]);
  const s = SCENARIOS[active];
  const typed = useTyped(s.prompt);

  useEffect(() => {
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) setActive(Number((e.target as HTMLElement).dataset.index));
        }
      },
      { rootMargin: "-45% 0px -45% 0px" },
    );
    items.current.forEach((n) => n && io.observe(n));
    return () => io.disconnect();
  }, []);

  const jump = (i: number) => items.current[i]?.scrollIntoView({ behavior: "smooth", block: "center" });

  return (
    <section className="lp-show" id="how">
      <div className="lp-show-grid">
        <div className="lp-show-sticky">
          <div className="lp-show-panel">
            {SCENARIOS.map((sc, i) => (
              <div key={sc.tab} className={`lp-show-bg lp-scene lp-scene-${i}${i === active ? " is-on" : ""}`} aria-hidden><span className="lp-scene-a" /><span className="lp-scene-b" /></div>
            ))}
            <div className="lp-show-grain" aria-hidden />
            <div className="lp-result" key={active}>
              <span className="lp-result-kicker">Result</span>
              <b>{s.result.title}</b>
              {s.result.lines.map((l) => <span key={l}>{l}</span>)}
            </div>
            <div className="lp-prompt">
              <div className="lp-tabs" role="tablist" aria-label="Examples">
                {SCENARIOS.map((sc, i) => (
                  <button key={sc.tab} role="tab" aria-selected={i === active} className={i === active ? "is-on" : undefined} onClick={() => jump(i)}>
                    {sc.tab}
                  </button>
                ))}
              </div>
              <div className="lp-prompt-row">
                <span className="lp-prompt-icon" aria-hidden>✳</span>
                <span className="lp-prompt-text">{typed}<span className="lp-caret" aria-hidden /></span>
                <span className="lp-prompt-go" aria-hidden>→</span>
              </div>
            </div>
          </div>
        </div>
        <div className="lp-show-list">
          <div className="lp-show-intro" data-reveal>
            <span className="lp-eyebrow">How it works</span>
            <h2>Say what you need. Watch it run.</h2>
            <p>Every workflow is a row of cards you can see and change. Each card does one thing, and the result of one feeds the next.</p>
          </div>
          {SCENARIOS.map((sc, i) => (
            <div key={sc.tab} data-index={i} ref={(n) => { items.current[i] = n; }} className="lp-show-item">
              <div className="lp-show-item-head">
                <span>{String(i + 1).padStart(2, "0")}</span>
                <b>{sc.tab}</b>
                <span className="lp-muted">{sc.prompt}</span>
              </div>
              <FlowCanvas s={sc} active={i === active} />
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
