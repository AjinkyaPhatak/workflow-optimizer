"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { AppLogo } from "./Brand";
import { ARC_CARDS, HERO_WORDS, type ArcCard } from "./data";

/** Flat colours the headline's word takes in turn. */
const WORD_COLORS = ["#2a9d8f", "#e76f51", "#264653", "#d9822b"];
const wordColor = (i: number) => WORD_COLORS[Math.max(0, i) % WORD_COLORS.length];

const reducedMotion = () => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/**
 * The headline's changing word, drawn on a canvas so it can dissolve block
 * by block into the next word (old blocks vanish, a few flicker as solid
 * squares, new blocks appear).
 */
function PixelWord({ words }: { words: string[] }) {
  const wrap = useRef<HTMLSpanElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [index, setIndex] = useState(0);
  const [width, setWidth] = useState<number | null>(null);

  useEffect(() => {
    const el = wrap.current, cv = canvas.current;
    if (!el || !cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    let stopped = false, raf = 0, timer = 0, current = 0;
    let font = "", spacing = "0px", fontSize = 0, height = 0, widths: number[] = [];
    const dpr = Math.min(window.devicePixelRatio || 1, 2);

    // Same font and letter spacing as the headline around it.
    const setFont = (target: CanvasRenderingContext2D) => {
      target.font = font;
      if ("letterSpacing" in target) target.letterSpacing = spacing;
    };

    // Each word in its own flat theme colour.
    const paint = (target: CanvasRenderingContext2D, word: string) => {
      setFont(target);
      target.textBaseline = "alphabetic";
      target.fillStyle = wordColor(words.indexOf(word));
      target.fillText(word, 0, height * 0.78);
    };

    const offscreen = (word: string, w: number) => {
      const c = document.createElement("canvas");
      c.width = Math.ceil(w * dpr);
      c.height = Math.ceil(height * dpr);
      const x = c.getContext("2d")!;
      x.scale(dpr, dpr);
      paint(x, word);
      return c;
    };

    const measure = () => {
      const cs = getComputedStyle(el);
      fontSize = parseFloat(cs.fontSize);
      font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      spacing = cs.letterSpacing === "normal" ? "0px" : cs.letterSpacing;
      height = Math.ceil(fontSize * 1.18);
      setFont(ctx);
      widths = words.map((w) => Math.ceil(ctx.measureText(w).width + fontSize * 0.08));
      const max = Math.max(...widths);
      cv.width = Math.ceil(max * dpr);
      cv.height = Math.ceil(height * dpr);
      cv.style.width = `${max}px`;
      cv.style.height = `${height}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, max, height);
      paint(ctx, words[current]);
      setWidth(widths[current]);
    };

    const dissolve = (from: number, to: number) => {
      const max = Math.max(...widths);
      const a = offscreen(words[from], max), b = offscreen(words[to], max);
      const block = Math.max(4, Math.round(fontSize / 11));
      const cols = Math.ceil(max / block), rows = Math.ceil(height / block);
      const ink = (c: HTMLCanvasElement) => {
        const data = c.getContext("2d")!.getImageData(0, 0, c.width, c.height).data;
        const out = new Uint8Array(cols * rows);
        for (let r = 0; r < rows; r++) for (let q = 0; q < cols; q++) {
          const px = Math.min(c.width - 1, Math.round((q + 0.5) * block * dpr));
          const py = Math.min(c.height - 1, Math.round((r + 0.5) * block * dpr));
          out[r * cols + q] = data[(py * c.width + px) * 4 + 3] > 40 ? 1 : 0;
        }
        return out;
      };
      const inkA = ink(a), inkB = ink(b);
      const seed = Array.from({ length: cols * rows }, (_, i) => Math.random() * 0.7 + (i % cols) / cols * 0.3);
      setWidth(Math.max(widths[from], widths[to]));
      const start = performance.now(), duration = 900, band = 0.18;
      const frame = (now: number) => {
        if (stopped) return;
        const p = Math.min(1, (now - start) / duration) * (1 + band);
        ctx.clearRect(0, 0, max, height);
        for (let r = 0; r < rows; r++) for (let q = 0; q < cols; q++) {
          const i = r * cols + q, t = seed[i];
          const sx = q * block, sy = r * block;
          if (t < p - band) {
            ctx.drawImage(b, sx * dpr, sy * dpr, block * dpr, block * dpr, sx, sy, block, block);
          } else if (t > p) {
            ctx.drawImage(a, sx * dpr, sy * dpr, block * dpr, block * dpr, sx, sy, block, block);
          } else if (inkA[i] || inkB[i]) {
            ctx.fillStyle = (q + r) % 3 === 0 ? "#e9c46a" : "#0e1316";
            ctx.fillRect(sx, sy, block, block);
          }
        }
        if (p < 1 + band) raf = requestAnimationFrame(frame);
        else {
          ctx.clearRect(0, 0, max, height);
          ctx.drawImage(b, 0, 0, b.width, b.height, 0, 0, max, height);
          setWidth(widths[to]);
        }
      };
      raf = requestAnimationFrame(frame);
    };

    const tick = () => {
      const next = (current + 1) % words.length;
      if (reducedMotion()) {
        current = next;
        measure();
      } else {
        dissolve(current, next);
        current = next;
      }
      setIndex(current);
      timer = window.setTimeout(tick, 2800);
    };

    // Measure only once the headline's own font has loaded (fonts.ready can
    // resolve before it starts loading).
    const cs = getComputedStyle(el);
    document.fonts.load(`${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`, words.join("")).catch(() => []).then(() => {
      if (stopped) return;
      measure();
      timer = window.setTimeout(tick, 2200);
    });
    const onResize = () => measure();
    window.addEventListener("resize", onResize);
    return () => {
      stopped = true;
      cancelAnimationFrame(raf);
      clearTimeout(timer);
      window.removeEventListener("resize", onResize);
    };
  }, [words]);

  return (
    <span ref={wrap} className="lp-word" style={width ? { width } : undefined}>
      <span className="lp-sr">{words[index]}</span>
      <canvas ref={canvas} aria-hidden />
      <svg key={index} className="lp-squiggle" viewBox="0 0 200 16" preserveAspectRatio="none" aria-hidden style={{ color: wordColor(index) }}>
        <path d="M2 10 C 20 3, 34 15, 52 9 S 86 3, 104 9 S 138 15, 156 8 S 186 4, 198 9" />
      </svg>
    </span>
  );
}

function CardFace({ card }: { card: ArcCard }) {
  switch (card.kind) {
    case "app":
      return (
        <>
          <span className="lp-arc-logo"><AppLogo app={card.app} size={40} /></span>
          <span className="lp-arc-label">{card.label}</span>
        </>
      );
    case "note":
      return (
        <>
          <span className="lp-arc-title">{card.title}</span>
          {card.lines.map((l) => <span key={l} className="lp-arc-line">{l}</span>)}
        </>
      );
    case "flow":
      return (
        <>
          <span className="lp-arc-flow">
            {card.steps.map((s, i) => (
              <span key={i} className="lp-arc-node"><AppLogo app={s} size={20} /></span>
            ))}
          </span>
          <span className="lp-arc-label">{card.label}</span>
        </>
      );
    case "stat":
      return (
        <>
          <span className="lp-arc-big">{card.big}</span>
          <span className="lp-arc-label">{card.label}</span>
        </>
      );
  }
}

/**
 * The curved carousel: cards on the inside of a cylinder seen from behind its
 * centre, so the middle cards sit far away and the edge cards come close,
 * drifting sideways forever.
 */
function ArcCarousel() {
  const stage = useRef<HTMLDivElement>(null);
  const cards = useRef<(HTMLDivElement | null)[]>([]);

  useEffect(() => {
    const el = stage.current;
    if (!el) return;
    const n = ARC_CARDS.length, step = 18, span = n * step;
    let offset = 0, last = performance.now(), raf = 0, visible = true;
    const still = reducedMotion();

    let width = 0;
    const layout = () => {
      const w = el.clientWidth;
      if (w !== width && el.parentElement) {
        width = w;
        el.parentElement.style.perspective = `${Math.round(w * 0.3)}px`;
      }
      const radius = w * 0.62;
      const near = radius * 0.12;
      for (let i = 0; i < n; i++) {
        const node = cards.current[i];
        if (!node) continue;
        let a = (i * step + offset) % span;
        if (a < 0) a += span;
        a -= span / 2;
        const rad = (a * Math.PI) / 180;
        const x = radius * Math.sin(rad);
        const z = near - radius * Math.cos(rad);
        const fade = Math.max(0, Math.min(1, (96 - Math.abs(a)) / 14));
        node.style.transform = `translate(-50%, -50%) translate3d(${x.toFixed(1)}px, 0, ${z.toFixed(1)}px) rotateY(${(-a).toFixed(2)}deg)`;
        node.style.opacity = fade.toFixed(3);
        node.style.zIndex = String(Math.round(1000 + z));
      }
    };

    const frame = (now: number) => {
      const dt = Math.min(64, now - last);
      last = now;
      if (visible && !still) offset -= dt * 0.0045;
      layout();
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);
    const io = new IntersectionObserver(([e]) => { visible = e.isIntersecting; });
    io.observe(el);
    return () => {
      cancelAnimationFrame(raf);
      io.disconnect();
    };
  }, []);

  return (
    <div className="lp-arc" aria-hidden>
      <div className="lp-arc-stage" ref={stage}>
        {ARC_CARDS.map((card, i) => (
          <div key={i} ref={(n) => { cards.current[i] = n; }} className={`lp-arc-card ${card.tone} k-${card.kind}`}>
            <CardFace card={card} />
          </div>
        ))}
      </div>
    </div>
  );
}

export function Hero({ signedIn }: { signedIn: boolean }) {
  return (
    <section className="lp-hero">
      <h1 className="lp-h1 lp-intro">
        <span className="lp-sr">Automate any task you can describe</span>
        <span aria-hidden className="lp-h1-line">Automate any <PixelWord words={HERO_WORDS} /></span>
        <span aria-hidden className="lp-h1-line">you can describe</span>
      </h1>
      <ArcCarousel />
      <div className="lp-hero-copy lp-intro lp-intro-late">
        <p className="lp-lede"><b>AI workflows for everyday work.</b></p>
        <p className="lp-sub">Connect Gmail, Calendar, Sheets and AI into simple step-by-step workflows. No code, and free to start.</p>
        <div className="lp-cta-row">
          <Link className="lp-btn lp-btn-primary" href={signedIn ? "/home" : "/login"}>
            {signedIn ? "Open app" : "Get started for free"}
          </Link>
          <Link className="lp-btn lp-btn-ghost" href="/templates">
            Browse templates
          </Link>
        </div>
      </div>
    </section>
  );
}
