"use client";

import Link from "next/link";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { CommitStrategy, useScribe } from "@elevenlabs/react";
import { useAssistant, materializePlan } from "@/features/assistant/useAssistant";
import { voiceErrorMessage } from "@/features/assistant/voiceErrors";
import { assistantApi } from "@/lib/api/assistant";
import { download } from "@/lib/api/client";
import { AiMarkdown, cleanAiText } from "@/components/ui/AiMarkdown";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { FriendlyValue } from "@/components/run/FriendlyValue";
import { ClarificationCard } from "./ClarificationCard";
import { WorkflowDraft } from "./WorkflowDraft";
import { CompilerIssues } from "./CompilerIssues";
import type { Budget, Plan } from "@/types/automation";

const EXAMPLES = [
  { title: "Make sense of the market", text: "Check the latest published Nifty 50 gainers and losers, make a CSV, calculate descriptive statistics, and send the files to Phatak on WhatsApp." },
  { title: "Put your knowledge to work", text: "Search my knowledge base and turn the relevant information into a useful report." },
  { title: "Keep something moving", text: "Build an automation that reads a web page every weekday, summarizes what changed, and emails me." },
];
function BudgetEditor({ budget, onChange }: { budget: Budget; onChange: (value: Budget) => void }) {
  return <div className="budget-fields">
    <label>Actions<input aria-label="Action limit" type="number" min={1} value={budget.max_actions || ""} onChange={e => onChange({ ...budget, max_actions: Number(e.target.value) })} /></label>
    <label>Tokens<input aria-label="Token limit" type="number" min={1} value={budget.max_tokens || ""} onChange={e => onChange({ ...budget, max_tokens: Number(e.target.value) })} /></label>
    <label>Active minutes<input aria-label="Active time limit" type="number" min={1} value={budget.max_active_seconds ? budget.max_active_seconds / 60 : ""} onChange={e => onChange({ ...budget, max_active_seconds: Number(e.target.value) * 60 })} /></label>
  </div>;
}
function PlanBudgetEditor({ plan, onSave, busy }: { plan: Plan; onSave: (plan: Plan) => Promise<void>; busy: boolean }) {
  const [budget, setBudget] = useState(plan.budget);
  return <><BudgetEditor budget={budget} onChange={setBudget} /><button disabled={busy || budget.max_actions < 1 || budget.max_tokens < 1 || budget.max_active_seconds < 1}
    onClick={() => void onSave({ ...plan, budget })}>Use these limits</button></>;
}
function ArtifactDownloads({ value }: { value: unknown }) {
  const [error, setError] = useState<unknown>(null);
  const found: { id: string; name: string }[] = [];
  const visit = (item: unknown) => {
    if (Array.isArray(item)) { item.forEach(visit); return; }
    if (!item || typeof item !== "object") return;
    const record = item as Record<string, unknown>;
    if (typeof record.id === "string" && typeof record.name === "string" && typeof record.content_type === "string") found.push({ id: record.id, name: record.name });
    else Object.values(record).forEach(visit);
  };
  visit(value);
  return <div className="artifact-downloads"><ErrorBanner error={error} />
    {[...new Map(found.map(file => [file.id, file])).values()].map(file => <button key={file.id} onClick={async () => {
      try {
        const blob = await download("/files/" + encodeURIComponent(file.id) + "/content");
        const url = URL.createObjectURL(blob); const link = document.createElement("a");
        link.href = url; link.download = file.name; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
      } catch (err) { setError(err); }
    }}>↓ {file.name}</button>)}
  </div>;
}
function ResultPreview({ value }: { value: Record<string, unknown> }) {
  if (typeof value.text === "string") return <FriendlyValue value={value.text} />;
  if (value.receipt && typeof value.receipt === "object") {
    const receipt = value.receipt as Record<string, unknown>;
    return <p className="muted">{receipt.status === "manually_reconciled" ? "Delivery reviewed." : "Message " + String(receipt.status ?? "sent") + "."}</p>;
  }
  const visible = Object.fromEntries(Object.entries(value).filter(([key]) => !["artifact", "artifacts", "child_run_id"].includes(key)));
  return Object.keys(visible).length ? <FriendlyValue value={visible} /> : null;
}
export function AssistantHome() {
  const s = useAssistant();
  const [text, setText] = useState("");
  const [speakReplies, setSpeakReplies] = useState(true);
  const [voiceTokenPending, setVoiceTokenPending] = useState(false);
  const [voiceError, setVoiceError] = useState<unknown>(null);
  const [scheduleOpen, setScheduleOpen] = useState(false);
  const [selectedStep, setSelectedStep] = useState("");
  const [cron, setCron] = useState("0 9 * * 1-5");
  const [timezone, setTimezone] = useState("Asia/Kolkata");
  const [scheduleSaved, setScheduleSaved] = useState(false);
  const [renewBudget, setRenewBudget] = useState<Budget>({ max_actions: 0, max_tokens: 0, max_active_seconds: 0 });
  const audio = useRef<HTMLAudioElement | null>(null);
  const audioURL = useRef("");
  const speech = useRef<AbortController | null>(null);
  const spoken = useRef("");
  const voiceRequest = useRef<AbortController | null>(null);
  const liveQuestion = s.run?.question ?? s.session?.compilation?.plan.questions.find(q => !Object.hasOwn(s.session?.answers ?? {}, q.id));
  // The SDK installs callbacks once per connection. Read the current draft and
  // question when a transcript arrives, including after a previous voice turn.
  const voiceContext = useRef({ s, liveQuestion });
  useEffect(() => { voiceContext.current = { s, liveQuestion }; });
  function stopSpeech() {
    speech.current?.abort(); audio.current?.pause(); audio.current = null;
    if (audioURL.current) URL.revokeObjectURL(audioURL.current); audioURL.current = "";
  }
  const scribe = useScribe({
    modelId: "scribe_v2_realtime", commitStrategy: CommitStrategy.VAD, vadSilenceThresholdSecs: 1.5,
    microphone: { echoCancellation: true, noiseSuppression: true },
    onPartialTranscript: ({ text: partial }) => {
      const current = voiceContext.current.s;
      if (partial.trim()) { stopSpeech(); if (current.busy) current.interrupt(); }
    },
    onCommittedTranscript: ({ text: committed }) => {
      if (!committed.trim()) return;
      const { s, liveQuestion } = voiceContext.current;
      stopSpeech();
      if (/^(stop|cancel|stop working|cancel that)[.! ]*$/i.test(committed.trim())) {
        s.interrupt();
        if (s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status))
          void assistantApi.control(s.run.id, "cancel").catch(s.setError);
        return;
      }
      if (liveQuestion) {
        let answer: unknown = committed.trim();
        if (liveQuestion.kind === "number") answer = Number(committed.replace(/,/g, ""));
        if (liveQuestion.kind === "boolean") {
          if (/^(yes|true|sure|correct)[.! ]*$/i.test(committed)) answer = true;
          else if (/^(no|false|incorrect)[.! ]*$/i.test(committed)) answer = false;
        }
        const match = liveQuestion.options?.find(option => option.label.toLowerCase() === committed.trim().toLowerCase() || option.value === committed.trim());
        if (match) answer = match.value;
        void s.answer(liveQuestion.id, answer).catch(s.setError);
      } else {
        if (s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status))
          void assistantApi.control(s.run.id, "pause").catch(s.setError);
        void s.send(committed).then(sent => { if (!sent) setText(current => [current, committed].filter(Boolean).join("\n")); });
      }
    },
    onError: error => failVoice(voiceErrorMessage(error)),
    onAuthError: () => failVoice("ElevenLabs rejected the voice connection. Check the ElevenLabs API key, or continue by typing."),
    onQuotaExceededError: () => failVoice("ElevenLabs voice credits have run out. You can continue by typing."),
    onRateLimitedError: () => failVoice("ElevenLabs is handling too many voice requests. Try again shortly, or type below."),
    onResourceExhaustedError: () => failVoice("ElevenLabs is busy. Try again shortly, or continue by typing."),
    onUnacceptedTermsError: () => failVoice("Accept the speech-to-text terms in your ElevenLabs account, then try voice again. You can still type below."),
    onDisconnect: stopSpeech,
  });
  const voiceActive = scribe.isConnected || scribe.isTranscribing;
  const voiceStarting = voiceTokenPending || scribe.status === "connecting";
  const { status: voiceStatus, disconnect: disconnectVoice } = scribe;
  function failVoice(message: string) { setVoiceError((previous: unknown) => previous ?? new Error(message)); scribe.disconnect(); stopSpeech(); }
  useEffect(() => {
    if (voiceStatus !== "connecting") return;
    const timer = setTimeout(() => {
      setVoiceError(new Error("The voice connection timed out. Try again or continue by typing.")); disconnectVoice();
    }, 20_000);
    return () => clearTimeout(timer);
  }, [voiceStatus, disconnectVoice]);
  useEffect(() => {
    if (!voiceActive || !speakReplies || !s.workspaceId) return;
    const message = liveQuestion?.prompt ?? s.session?.messages.filter(m => m.role === "assistant").at(-1)?.text ?? "";
    const key = liveQuestion?.id ?? (s.session?.id + ":" + s.session?.revision);
    if (!message || spoken.current === key) return;
    spoken.current = key;
    const controller = new AbortController(); speech.current = controller;
    assistantApi.speak(s.workspaceId, cleanAiText(message).slice(0, 700), controller.signal).then(blob => {
      if (controller.signal.aborted) return;
      if (audioURL.current) URL.revokeObjectURL(audioURL.current);
      audioURL.current = URL.createObjectURL(blob); audio.current = new Audio(audioURL.current);
      return audio.current.play();
    }).catch(err => { if ((err as Error).name !== "AbortError") setVoiceError(new Error("The spoken reply is unavailable. The full reply is shown here.")); });
    return () => controller.abort();
  }, [liveQuestion?.id, liveQuestion?.prompt, s.session?.id, s.session?.revision, s.session?.messages, voiceActive, speakReplies, s.workspaceId]);
  useEffect(() => () => { voiceRequest.current?.abort(); speech.current?.abort(); audio.current?.pause(); if (audioURL.current) URL.revokeObjectURL(audioURL.current); }, []);
  async function toggleVoice() {
    if (voiceActive) { scribe.disconnect(); stopSpeech(); return; }
    if (!s.workspaceId || voiceRequest.current || voiceStarting) return;
    setVoiceError(null);
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
      setVoiceError(new Error("The browser cannot access a microphone here. Open this app on localhost or HTTPS, or continue by typing.")); return;
    }
    const controller = new AbortController(); voiceRequest.current = controller; setVoiceTokenPending(true);
    try {
      const token = await assistantApi.voiceToken(s.workspaceId, controller.signal);
      if (!controller.signal.aborted) await scribe.connect({ token: token.token });
    } catch (error) { if (!controller.signal.aborted) failVoice(voiceErrorMessage(error)); }
    finally { if (voiceRequest.current === controller) { voiceRequest.current = null; setVoiceTokenPending(false); } }
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!s.workspaceId || !text.trim()) return;
    const message = text; stopSpeech();
    if (await s.send(message)) setText(current => current === message ? "" : current);
  }
  const compilation = s.session?.compilation;
  const activeRun = s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status);
  return <main className={"assistant-home " + (compilation ? "has-workflow" : "")}>
    <aside className="assistant-sidebar">
      <button className="new-conversation" onClick={s.fresh}>＋ New request</button>
      {s.workspaces.length > 1 && <select aria-label="Workspace" value={s.workspaceId} onChange={e => { s.fresh(); s.setWorkspaceId(e.target.value); }}>
        {s.workspaces.map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
      </select>}
      <span className="eyebrow">Recent workflows</span>
      {s.sessions.length === 0 && <p className="hint">Your ideas will take shape here.</p>}
      {s.sessions.map(session => <button key={session.id} className={"conversation-link " + (s.session?.id === session.id ? "selected" : "")} onClick={() => void s.open(session.id)}>
        <span>◇</span>{session.compilation?.plan.name ?? "New conversation"}
      </button>)}
      <div className="sidebar-footer"><Link href="/automations">Schedules & triggers ↗</Link><Link href="/knowledge">Add some knowledge ↗</Link><Link href="/settings/whatsapp">Connect WhatsApp ↗</Link></div>
    </aside>
    <section className="assistant-conversation">
      <div className="conversation-scroll">
        {!s.session?.messages.length && !s.run ? <div className="assistant-welcome">
          <span className="welcome-orbit" aria-hidden>✦</span><div className="eyebrow">A little less busywork</div>
          <h1>Say what you need.<br /><span>Let’s make it happen.</span></h1>
          <p>Talk it through, shape the steps, and let your workflow take it from here.</p>
          <div className="suggestion-cards">{EXAMPLES.map((example, i) => <button key={example.title} onClick={() => setText(example.text)}>
            <span className="suggestion-number">0{i + 1}</span><strong>{example.title}</strong><span>{example.text}</span><b aria-hidden>↗</b>
          </button>)}</div>
        </div> : <div className="conversation-messages" aria-live="polite">
          {s.session?.messages.map((message, i) => <article key={i} className={"assistant-message " + message.role}>
            <span className="message-author">{message.role === "user" ? "You" : "✦ Workflow"}</span><AiMarkdown text={message.text} />
          </article>)}
        </div>}
        {s.pending && <article className="assistant-message user"><span className="message-author">You</span><p>{s.pending}</p></article>}
        {s.busy && !s.repairing && <div className="planning-progress" role="status"><span className="spinner" />Shaping the steps and checking what’s needed…</div>}
        <ErrorBanner error={s.error} />
        {compilation && (!s.busy || s.repairing) && <CompilerIssues compilation={compilation} failure={s.doctorFailure} />}
        {liveQuestion && <ClarificationCard key={liveQuestion.id} question={liveQuestion} onAnswer={value => s.answer(liveQuestion.id, value)} busy={s.busy} />}
        {liveQuestion?.input === "config.credential_id" && !liveQuestion.options?.length && <p className="hint">
          <Link href="/settings/connected-accounts" target="_blank">Connect the account</Link> or <Link href="/settings/credentials" target="_blank">add an AI key</Link>, then{" "}
          <button className="link" disabled={s.busy} onClick={() => void s.send("I have connected the required account. Check the available accounts and update this workflow.")}>check again</button>.
        </p>}
        {compilation?.plan.mode === "continuous" && <section className="continuous-budget">
          <h3>How much room should I work within?</h3><p className="hint">Work pauses when any limit is reached. Time spent waiting for you does not count.</p>
          <PlanBudgetEditor key={s.session?.revision} plan={compilation.plan} onSave={s.edit} busy={s.busy} />
        </section>}
        {s.run && <section className="assistant-run">
          <div className="row"><span className={"run-status status-" + s.run.status.toLowerCase()}>{s.run.status.replaceAll("_", " ").toLowerCase()}</span><span className="spacer" />
            {activeRun && <><button className="small-button" onClick={() => void assistantApi.control(s.run!.id, s.run!.status === "PAUSED" ? "resume" : "pause").catch(s.setError)}>{s.run.status === "PAUSED" ? "Resume" : "Pause"}</button><button className="small-button danger" onClick={() => void assistantApi.control(s.run!.id, "cancel").catch(s.setError)}>Stop</button></>}
          </div>
          <ol className="run-progress">{s.events.filter(event => event.run_id === s.run?.id && event.kind === "step_completed").map(event => <li key={event.id}><span>✓</span>{event.message || event.step_id}</li>)}</ol>
          {s.run.error && <p className="compiler-finding">{s.run.error}</p>}
          {s.run.status === "PAUSED" && <div className="continuous-budget"><h3>Give this work a new budget</h3><BudgetEditor budget={renewBudget} onChange={setRenewBudget} />
            <button onClick={() => void assistantApi.control(s.run!.id, "resume", renewBudget).catch(s.setError)}>Continue with these limits</button></div>}
          {s.run.status === "WAITING_FOR_EVENT" && <button onClick={() => void assistantApi.event(s.run!.id, { continued: true }).catch(s.setError)}>Continue now</button>}
          <ArtifactDownloads value={s.run.outputs} />
          {Object.values(s.run.outputs).filter(value => typeof value.child_run_id === "string").map(value =>
            <p key={String(value.child_run_id)}><Link href={"/home?run=" + encodeURIComponent(String(value.child_run_id))}>Open the linked workflow to see its progress or answer its questions ↗</Link></p>)}
          {s.run.status === "COMPLETED" && Object.entries(s.run.outputs).filter(([id]) => !id.startsWith("_")).slice(-2).map(([id, value]) => <div key={id} className="run-result"><ResultPreview value={value} /></div>)}
        </section>}
        {compilation?.ready && <div className="workflow-actions">
          <button className="primary" disabled={s.busy || !!activeRun} onClick={() => void s.start()}>{s.run ? "Run again" : "Run workflow"}</button>
          <button onClick={() => void s.fork()}>Fork</button><button onClick={() => setScheduleOpen(!scheduleOpen)}>Make recurring</button>
        </div>}
        {scheduleOpen && compilation && <form className="schedule-card" onSubmit={async e => {
          e.preventDefault();
          try { await assistantApi.saveAutomation({ workspace_id: s.workspaceId, name: compilation.plan.name, plan: materializePlan(compilation.plan, s.session?.answers ?? {}),
            ...(s.credentialId ? { credential_id: s.credentialId } : {}), cron, timezone, trigger: "", enabled: true }); setScheduleSaved(true); } catch (err) { s.setError(err); }
        }}>
          <h3>Keep this workflow running</h3><label>Cron expression<input value={cron} onChange={e => setCron(e.target.value)} /></label><p className="hint">0 9 * * 1-5 runs at 9 am every weekday.</p>
          <label>Timezone<input value={timezone} onChange={e => setTimezone(e.target.value)} /></label><button type="submit" className="primary">Save schedule</button>
          {scheduleSaved && <p role="status">Schedule saved. Manage it in <Link href="/automations">Schedules & triggers</Link>.</p>}
        </form>}
      </div>
      <form className="voice-composer" onSubmit={submit}>
        <ErrorBanner error={voiceError} />
        {!!s.workspaceError && <div role="status"><ErrorBanner error={s.workspaceError} /><button type="button" disabled={s.loading} onClick={s.reloadWorkspace}>Retry loading workspace</button></div>}
        {s.loading && !s.workspaceId && <p className="hint" role="status">Loading your workspace. You can write your request while you wait.</p>}
        {voiceActive && <div className="live-transcript" role="status"><span className="listening-dot" />{scribe.partialTranscript || "Listening. You can interrupt me anytime."}</div>}
        <textarea aria-label="What would you like to do?" placeholder="Tell me what you’d like to get done…" value={text} onChange={e => setText(e.target.value)} rows={2}
          onKeyDown={e => { if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); if (text.trim() && s.workspaceId) e.currentTarget.form?.requestSubmit(); } }} />
        <div className="composer-toolbar"><button type="button" disabled={!s.workspaceId || voiceStarting} className={"mic-button " + (voiceActive ? "listening" : "")} aria-label={voiceStarting ? "Connecting microphone" : voiceActive ? "Stop microphone" : "Start microphone"} aria-pressed={voiceActive} onClick={() => void toggleVoice()}>
          <svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="9" y="2" width="6" height="12" rx="3" /><path d="M5 10v2a7 7 0 0014 0v-2M12 19v3M8 22h8" /></svg>
        </button><span className="hint">{voiceStarting ? "Connecting…" : voiceActive ? "Tap to stop" : "Talk it through"}</span><span className="spacer" />
          <button className="primary send-button" type="submit" disabled={!text.trim() || !s.workspaceId}>Make it happen ↑</button>
        </div>
        <div className="composer-settings"><label><input type="checkbox" checked={s.autoExecute} onChange={e => s.setAutoExecute(e.target.checked)} /> Run when ready</label>
          <label><input type="checkbox" checked={speakReplies} onChange={e => { setSpeakReplies(e.target.checked); if (!e.target.checked) stopSpeech(); }} /> Spoken replies</label>
          {!s.session && <select aria-label="AI connection" value={s.credentialId} onChange={e => s.setCredentialId(e.target.value)}><option value="">Platform AI</option>{s.credentials.map(key => <option key={key.id} value={key.id}>{key.name}</option>)}</select>}
        </div>
      </form>
    </section>
    {compilation && <aside className="assistant-builder"><WorkflowDraft key={s.session!.id + ":" + s.session!.revision} plan={compilation.plan} tools={s.tools} comparisons={s.comparisons} selectedStep={selectedStep} onSelectStep={setSelectedStep} busy={s.busy} onSave={s.edit} /></aside>}
  </main>;
}
