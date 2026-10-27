"use client";

import { useEffect, useRef, useState } from "react";
import { AppLogo } from "./Brand";
import { APP_WALL } from "./data";

const ASKS = [
  "Summarize my unread email",
  "Brief me on today's meetings",
  "Turn this recording into notes",
];

/** A chat bubble that types out requests, one after another. */
function TypingBubble() {
  const [text, setText] = useState("");
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let ask = 0, i = 0, timer = 0, deleting = false, running = false;
    const step = () => {
      const target = ASKS[ask];
      if (!deleting) {
        i++;
        setText(target.slice(0, i));
        if (i >= target.length) { deleting = true; timer = window.setTimeout(step, 1800); return; }
        timer = window.setTimeout(step, 45);
      } else {
        i -= 2;
        setText(target.slice(0, Math.max(0, i)));
        if (i <= 0) { deleting = false; ask = (ask + 1) % ASKS.length; timer = window.setTimeout(step, 350); return; }
        timer = window.setTimeout(step, 18);
      }
    };
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting && !running) { running = true; step(); }
      else if (!e.isIntersecting && running) { running = false; clearTimeout(timer); }
    });
    if (ref.current) io.observe(ref.current);
    return () => { clearTimeout(timer); io.disconnect(); };
  }, []);

  return (
    <div className="lp-bubble" ref={ref}>
      <span className="lp-bubble-text">{text}<span className="lp-caret" aria-hidden /></span>
      <span className="lp-bubble-send" aria-hidden>↑</span>
    </div>
  );
}

export function Apps() {
  return (
    <section className="lp-apps-wrap">
      <div className="lp-apps" data-reveal>
        <h2 className="lp-apps-title">Plugs into the tools you already use</h2>
        <div className="lp-apps-grid">
          <div className="lp-case">
            <div className="lp-case-art"><TypingBubble /></div>
            <span className="lp-case-kicker">Start from a sentence</span>
            <p className="lp-case-head">Describe the task, pick a template, press Run.</p>
            <a className="lp-case-link" href="#how">See how it works <span aria-hidden>→</span></a>
          </div>
          <ul className="lp-logos">
            {APP_WALL.map((a) => (
              <li key={a.name} className={a.soon ? "is-soon" : undefined}>
                <AppLogo app={a.app} size={26} />
                <span>{a.name}</span>
                {a.soon && <em>Soon</em>}
              </li>
            ))}
            <li className="lp-logos-more"><span>+ AI that writes, summarizes, translates and transcribes</span></li>
          </ul>
        </div>
      </div>
    </section>
  );
}
