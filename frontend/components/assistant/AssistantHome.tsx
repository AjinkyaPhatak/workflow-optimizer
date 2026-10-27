"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { WorkflowManagement } from "./WorkflowManagement";
import { useBuildProgress } from "@/features/assistant/useBuildProgress";
import { useEditorLayout } from "@/features/assistant/useEditorLayout";
import "./workspace.css";
import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { CommitStrategy, useScribe } from "@elevenlabs/react";
import { useAssistant, materializePlan } from "@/features/assistant/useAssistant";
import { PushToTalk, type TalkPhase } from "@/features/assistant/pushToTalk";
import { voiceErrorMessage } from "@/features/assistant/voiceErrors";
import { assistantApi } from "@/lib/api/assistant";
import { download } from "@/lib/api/client";
import { AiMarkdown, cleanAiText } from "@/components/ui/AiMarkdown";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { Backdrop } from "@/components/ui/Backdrop";
import { FriendlyValue } from "@/components/run/FriendlyValue";
import { ClarificationCard } from "./ClarificationCard";
import { WorkflowDraft } from "./WorkflowDraft";
import { CompilerIssues } from "./CompilerIssues";
import type { Budget, Plan } from "@/types/automation";

function BudgetEditor({ budget, onChange }: { budget: Budget; onChange: (value: Budget) => void }) {
  return <div className="budget-fields">
    <label>Actions<input aria-label="Action limit" type="number" min={1} value={budget.max_actions || ""} onChange={e => onChange({ ...budget, max_actions: Number(e.target.value) })} /></label>
    <label>Tokens<input aria-label="Token limit" type="number" min={1} value={budget.max_tokens || ""} onChange={e => onChange({ ...budget, max_tokens: Number(e.target.value) })} /></label>
    <label>Active minutes<input aria-label="Active time limit" type="number" min={1} value={budget.max_active_seconds ? budget.max_active_seconds / 60 : ""} onChange={e => onChange({ ...budget, max_active_seconds: Number(e.target.value) * 60 })} /></label>
  </div>;
}
function PlanBudgetEditor({ plan, onSave, busy }: { plan: Plan; onSave: (plan: Plan) => Promise<unknown>; busy: boolean }) {
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
export function AssistantHome({ workflowId }: { workflowId?: string } = {}) {
  const s = useAssistant(workflowId);
  const router = useRouter();
  const canEdit = s.workspaces.some(workspace => workspace.id === s.workspaceId && workspace.role !== "viewer");
  const progress = useBuildProgress(s.session?.id, s.session?.revision, s.building);
  const buildLabel = progress ? (progress.phase === "planning" ? "Planning the next steps" : progress.phase === "repairing" ? "Refining the workflow" : progress.phase === "saving" ? "Saving the checked workflow" : "Checking the generated steps") + (progress.pass > 1 ? " · pass " + progress.pass : "") : s.repairing ? "Checking and repairing the workflow…" : "Building your workflow…";
  const [conversationOpen, setConversationOpen] = useState(true);
  const flushRef = useRef<(() => Promise<void>) | null>(null);
  const flush = useCallback(async () => { await flushRef.current?.(); }, []);
  const registerFlush = useCallback((callback: (() => Promise<void>) | null) => { flushRef.current = callback; }, []);
  const layout = useEditorLayout(s.session?.id);
  const [workflowVersions, setWorkflowVersions] = useState<{id: string; name: string}[]>([]);
  useEffect(() => {
    if (!s.workspaceId) return;
    let live = true;
    assistantApi.workflows(s.workspaceId).then(result => { if (live) setWorkflowVersions(result.items.filter(w => w.active_version_id).map(w => ({ id: "version:" + w.active_version_id, name: w.name + " (published)" }))); }).catch(() => {});
    return () => { live = false; };
  }, [s.workspaceId, s.session?.revision]);
  async function afterSave(action: () => Promise<unknown>) { try { await flush(); await action(); } catch (error) { s.setError(error); } }

  const [text, setText] = useState("");
  const [speakReplies, setSpeakReplies] = useState(true);
  const [talkPhase, setTalkPhase] = useState<TalkPhase>("idle");
  const [voiceReplyTurn, setVoiceReplyTurn] = useState(0);
  const talkRef = useRef<PushToTalk | null>(null);
  const heldInput = useRef<string | number | null>(null);
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
  const liveQuestion = s.run?.question ?? s.session?.compilation?.plan.questions.find(q => q.purpose === "intent" || !Object.hasOwn(s.session?.answers ?? {}, q.id));
  // The SDK installs callbacks once per connection. Read the current draft and
  // question when a transcript arrives, including after a previous voice turn.
  const voiceContext = useRef({ s, liveQuestion });
  useEffect(() => { voiceContext.current = { s, liveQuestion }; });
  const stopSpeech = useCallback(() => {
    speech.current?.abort(); audio.current?.pause(); audio.current = null;
    if (audioURL.current) URL.revokeObjectURL(audioURL.current); audioURL.current = "";
  }, []);
  function submitVoice(committed: string) {
    setVoiceReplyTurn(turn => turn + 1);
    if (!committed.trim()) return;
    const { s, liveQuestion } = voiceContext.current;
    stopSpeech();
    if (/^(stop|cancel|stop working|cancel that)[.! ]*$/i.test(committed.trim())) {
      s.interrupt();
      if (s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status))
        void assistantApi.control(s.run.id, "cancel").catch(s.setError);
      return;
    }
    if (s.building) { setText(current => [current, committed].filter(Boolean).join("\n")); return; }
    if (liveQuestion) {
      let answer: unknown = committed.trim();
      if (liveQuestion.kind === "number") answer = Number(committed.replace(/,/g, ""));
      if (liveQuestion.kind === "boolean") {
        if (/^(yes|true|sure|correct)[.! ]*$/i.test(committed)) answer = true;
        else if (/^(no|false|incorrect)[.! ]*$/i.test(committed)) answer = false;
      }
      const match = liveQuestion.options?.find(option => option.label.toLowerCase() === committed.trim().toLowerCase() || option.value === committed.trim());
      if (match) answer = match.value;
      void flush().then(() => voiceContext.current.s.answer(liveQuestion.id, answer)).catch(error => { s.setError(error); setText(current => [current, committed].filter(Boolean).join("\n")); });
    } else {
      if (s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status))
        void assistantApi.control(s.run.id, "pause").catch(s.setError);
      void flush().then(() => voiceContext.current.s.send(committed)).then(sent => { if (!sent) setText(current => [current, committed].filter(Boolean).join("\n")); }).catch(error => { s.setError(error); setText(current => [current, committed].filter(Boolean).join("\n")); });
    }
  }
  const scribe = useScribe({
    modelId: "scribe_v2_realtime", commitStrategy: CommitStrategy.MANUAL,
    microphone: { echoCancellation: true, noiseSuppression: true },
    onPartialTranscript: ({ text: partial }) => { talkRef.current?.updatePartial(partial); },
    onCommittedTranscript: ({ text: committed }) => talkRef.current?.committed(committed),
    onError: error => failVoice(voiceErrorMessage(error)),
    onAuthError: () => failVoice("ElevenLabs rejected the voice connection. Check the ElevenLabs API key, or continue by typing."),
    onQuotaExceededError: () => failVoice("ElevenLabs voice credits have run out. You can continue by typing."),
    onRateLimitedError: () => failVoice("ElevenLabs is handling too many voice requests. Try again shortly, or type below."),
    onResourceExhaustedError: () => failVoice("ElevenLabs is busy. Try again shortly, or continue by typing."),
    onUnacceptedTermsError: () => failVoice("Accept the speech-to-text terms in your ElevenLabs account, then try voice again. You can still type below."),
    onDisconnect: () => talkRef.current?.disconnected(),
  });
  const voiceActive = talkPhase === "listening";
  const voiceStarting = talkPhase === "starting";
  const voiceDraining = talkPhase === "draining";
  const voiceIO = useRef({ scribe, submitVoice, workspaceId: s.workspaceId });
  useEffect(() => { voiceIO.current = { scribe, submitVoice, workspaceId: s.workspaceId }; });
  useEffect(() => {
    const talk = new PushToTalk({
      start: async signal => {
        setVoiceError(null); stopSpeech();
        if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia)
          throw new Error("The browser cannot access a microphone here. Open this app on localhost or HTTPS, or continue by typing.");
        let token;
        try { token = await assistantApi.voiceToken(voiceIO.current.workspaceId, signal); }
        catch (error) { throw new Error(voiceErrorMessage(error)); }
        if (signal.aborted) return;
        voiceIO.current.scribe.clearTranscripts();
        await voiceIO.current.scribe.connect({ token: token.token });
        if (signal.aborted) voiceIO.current.scribe.disconnect();
      },
      mute: () => voiceIO.current.scribe.mute(),
      commit: () => voiceIO.current.scribe.commit(),
      close: () => { heldInput.current = null; voiceIO.current.scribe.disconnect(); },
      changed: setTalkPhase,
      transcript: committed => voiceIO.current.submitVoice(committed),
      recover: partial => setText(current => [current, partial].filter(Boolean).join("\n")),
      error: error => setVoiceError(error instanceof Error ? error : new Error(voiceErrorMessage(error))),
    });
    talkRef.current = talk;
    return () => { talk.cancel(); talkRef.current = null; };
  }, [stopSpeech]);
  function failVoice(message: string) { talkRef.current?.fail(new Error(message)); }
  useEffect(() => { if (scribe.isConnected) talkRef.current?.ready(); }, [scribe.isConnected]);
  useEffect(() => {
    if (s.building || !canEdit) talkRef.current?.cancel();
  }, [s.building, canEdit]);
  useEffect(() => {
    const cancel = () => { if (heldInput.current !== null) { heldInput.current = null; talkRef.current?.cancel(); } };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") cancel(); };
    window.addEventListener("blur", cancel); window.addEventListener("keydown", escape);
    return () => { window.removeEventListener("blur", cancel); window.removeEventListener("keydown", escape); cancel(); };
  }, []);
  useEffect(() => {
    if (!voiceReplyTurn || talkPhase !== "idle" || !speakReplies || !s.workspaceId || s.building) return;
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
  }, [liveQuestion?.id, liveQuestion?.prompt, s.session?.id, s.session?.revision, s.session?.messages, voiceReplyTurn, talkPhase, speakReplies, s.workspaceId, s.building]);
  useEffect(() => () => { speech.current?.abort(); audio.current?.pause(); if (audioURL.current) URL.revokeObjectURL(audioURL.current); }, []);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!s.workspaceId || !text.trim() || s.building) return;
    const message = text; stopSpeech();
    try { await flush(); if (await voiceContext.current.s.send(message)) setText(current => current === message ? "" : current); } catch (error) { s.setError(error); }
  }
  const compilation = s.session?.compilation;
  const activeRun = s.run && !["COMPLETED", "FAILED", "CANCELLED"].includes(s.run.status);
  return <main className={"canvas-workspace " + (conversationOpen ? "conversation-open" : "conversation-closed")}>
    <aside className="workspace-conversation" hidden={!conversationOpen} aria-label="Conversation">
      <header className="row"><strong>Conversation</strong><span className="spacer" /><button aria-label="Collapse conversation" onClick={() => setConversationOpen(false)}>‹</button></header>
      <details className="workspace-recents"><summary>Recent workflows</summary>
        {s.workspaces.length > 1 && <select disabled={s.building} aria-label="Workspace" value={s.workspaceId} onChange={e => { s.fresh(); s.setWorkspaceId(e.target.value); }}>{s.workspaces.map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}</select>}
        {s.sessions.map(session => <button key={session.id} className="conversation-link" disabled={s.building} onClick={() => void afterSave(() => s.open(session.id))}>{session.compilation?.plan.name ?? "New workflow"}</button>)}
        {!s.sessions.length && <p className="small muted">Your saved workflows will appear here.</p>}
      </details>
      <div className="conversation-scroll">

        {!s.session?.messages.length && !s.run ? <p className="muted small">Describe what you need below. Your workflow will take shape on the canvas, and I’ll ask here if a detail is missing.</p> : <div className="conversation-messages" aria-live="polite">
          {s.session?.messages.map((message, i) => <article key={i} className={"assistant-message " + message.role}>
            <span className="message-author">{message.role === "user" ? "You" : "✦ Workflow"}</span><AiMarkdown text={message.text} />
          </article>)}
        </div>}
        {s.pending && <article className="assistant-message user"><span className="message-author">You</span><p>{s.pending}</p></article>}
        {s.busy && !s.repairing && <div className="planning-progress" role="status"><span className="spinner" />Shaping the steps and checking what’s needed…</div>}
        <ErrorBanner error={s.error} />
        {compilation && (!s.busy || s.repairing) && <CompilerIssues compilation={compilation} failure={s.doctorFailure} />}
        {liveQuestion && <ClarificationCard key={liveQuestion.id} question={liveQuestion} onAnswer={async value => { await flush(); await s.answer(liveQuestion.id, value); }} busy={s.busy} />}
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
          <button className="primary" disabled={s.busy || !!activeRun} onClick={() => void afterSave(s.start)}>{s.run ? "Run again" : "Run workflow"}</button>
          <button disabled={s.building} onClick={() => void afterSave(s.fork)}>Fork</button><button disabled={s.building} onClick={() => setScheduleOpen(!scheduleOpen)}>Make recurring</button>
        </div>}
        {scheduleOpen && compilation && <form className="schedule-card" onSubmit={async e => {
          e.preventDefault();
          try { await flush(); const saved = s.currentSession(); if (!saved?.compilation?.ready) throw new Error("Finish checking this workflow before scheduling it."); await assistantApi.saveAutomation({ workspace_id: saved.workspace_id, name: saved.compilation.plan.name, plan: materializePlan(saved.compilation.plan, saved.answers),
            ...(s.credentialId ? { credential_id: s.credentialId } : {}), cron, timezone, trigger: "", enabled: true }); setScheduleSaved(true); } catch (err) { s.setError(err); }
        }}>
          <h3>Keep this workflow running</h3><label>Cron expression<input value={cron} onChange={e => setCron(e.target.value)} /></label><p className="hint">0 9 * * 1-5 runs at 9 am every weekday.</p>
          <label>Timezone<input value={timezone} onChange={e => setTimezone(e.target.value)} /></label><button type="submit" className="primary" disabled={s.building}>Save schedule</button>
          {scheduleSaved && <p role="status">Schedule saved. Manage it in <Link href="/automations">Schedules & triggers</Link>.</p>}
        </form>}

      </div>
    </aside>
    <section className="workspace-stage" aria-label="Canvas workspace">
      <Backdrop />
      <WorkflowDraft plan={progress?.plan ?? compilation?.plan ?? s.run?.plan ?? null} tools={s.tools} comparisons={s.comparisons} controls={s.controls}
        sessionId={s.session?.id} revision={s.session?.revision} layout={layout.layout} onLayoutSave={layout.save}
        selectedStep={selectedStep} onSelectStep={setSelectedStep} busy={s.building} readOnly={!canEdit} onSave={s.edit} onManualEdit={s.beginManualEdit} onDirtyChange={dirty => { if (!dirty) s.endManualEdit(); }} onFlushReady={registerFlush}
        workflowVersions={workflowVersions} completedSteps={s.events.filter(event => event.run_id === s.run?.id && event.kind === "step_completed").map(event => event.step_id)}
        checkPlan={s.session ? plan => { const active = s.currentSession()!; return assistantApi.check(active.id, active.revision, plan); } : undefined}
        headerActions={<>
          <button aria-expanded={conversationOpen} aria-label={conversationOpen ? "Hide conversation" : "Show conversation"} onClick={() => setConversationOpen(!conversationOpen)}>Conversation{liveQuestion ? " •" : ""}</button>
          <button disabled={s.building} onClick={() => void afterSave(async () => { s.fresh(); if (workflowId) router.push("/home"); })}>New request</button>
          <WorkflowManagement session={s.session} currentSession={s.currentSession} flush={flush} onRestore={s.acceptSession} disabled={s.busy || !canEdit} />
          <button className="primary" disabled={!canEdit || !compilation?.ready || s.busy || !!activeRun} onClick={() => void afterSave(s.start)}>▶ Run</button>
        </>} />
      <form className="voice-composer" onSubmit={submit}>
        <ErrorBanner error={voiceError} />
        {s.building && <div className="row small" role="status"><span className="spinner" />{buildLabel}. Editing unlocks when this finishes.<span className="spacer" /><button type="button" onClick={s.interrupt}>Stop building</button></div>}
        {!conversationOpen && liveQuestion && <button type="button" className="composer-question" onClick={() => setConversationOpen(true)}>Answer needed: {liveQuestion.prompt}</button>}
        {!conversationOpen && (s.busy || !!s.doctorFailure || !!s.error) && <div className="small" role="status">{s.doctorFailure || (s.repairing ? "Checking and fixing your workflow…" : s.busy ? "Shaping your workflow…" : "A request needs attention.")} <button type="button" className="link" onClick={() => setConversationOpen(true)}>Open conversation</button></div>}
        {!!s.workspaceError && <div role="status"><ErrorBanner error={s.workspaceError} /><button type="button" disabled={s.loading} onClick={s.reloadWorkspace}>Retry loading workspace</button></div>}
        {s.loading && !s.workspaceId && <p className="hint" role="status">Loading your workspace. You can write your request while you wait.</p>}
        {voiceActive && <div className="live-transcript" role="status"><span className="listening-dot" />{scribe.partialTranscript || "Listening. Release to send."}</div>}
        <textarea aria-label="What would you like to do?" placeholder="Tell me what you’d like to get done…" value={text} onChange={e => setText(e.target.value)} rows={2}
          onKeyDown={e => { if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); if (text.trim() && s.workspaceId) e.currentTarget.form?.requestSubmit(); } }} />
        <div className="composer-toolbar"><button type="button" disabled={!canEdit || !s.workspaceId || s.building} aria-disabled={voiceDraining} className={"mic-button " + (voiceActive ? "listening" : "")} aria-label={voiceStarting ? "Connecting microphone. Release to cancel" : voiceActive ? "Release to send" : voiceDraining ? "Finishing transcript" : "Hold to speak"} aria-pressed={voiceActive || voiceStarting} aria-describedby="microphone-instructions" style={{ touchAction: "none" }}
          onPointerDown={e => { if (e.button !== 0 || heldInput.current !== null || voiceDraining) return; e.preventDefault(); e.currentTarget.focus(); e.currentTarget.setPointerCapture(e.pointerId); heldInput.current = e.pointerId; talkRef.current?.press(); }}
          onPointerUp={e => { if (heldInput.current !== e.pointerId) return; heldInput.current = null; talkRef.current?.release(); if (e.currentTarget.hasPointerCapture(e.pointerId)) e.currentTarget.releasePointerCapture(e.pointerId); }}
          onPointerCancel={() => { heldInput.current = null; talkRef.current?.cancel(); }}
          onLostPointerCapture={() => { if (heldInput.current !== null) { heldInput.current = null; talkRef.current?.cancel(); } }}
          onKeyDown={e => { if (e.key !== " " && e.key !== "Enter") return; e.preventDefault(); if (e.repeat || heldInput.current !== null || voiceDraining) return; heldInput.current = e.key; talkRef.current?.press(); }}
          onKeyUp={e => { if (e.key !== " " && e.key !== "Enter") return; e.preventDefault(); if (heldInput.current !== e.key) return; heldInput.current = null; talkRef.current?.release(); }}
          onBlur={() => { if (heldInput.current !== null) { heldInput.current = null; talkRef.current?.cancel(); } }}>
          <svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="9" y="2" width="6" height="12" rx="3" /><path d="M5 10v2a7 7 0 0014 0v-2M12 19v3M8 22h8" /></svg>
        </button><span className="hint" id="microphone-instructions">{voiceStarting ? "Keep holding while connecting. Release to cancel." : voiceActive ? "Release to send · Escape to cancel" : voiceDraining ? "Finishing transcript…" : "Hold to speak · Space or Enter also works"}</span><span className="spacer" />
          <button className="primary send-button" type="submit" disabled={!canEdit || !text.trim() || !s.workspaceId || s.building}>Make it happen ↑</button>
        </div>
        <div className="composer-settings"><label><input type="checkbox" checked={s.autoExecute} onChange={e => s.setAutoExecute(e.target.checked)} /> Run when ready</label>
          <label><input type="checkbox" checked={speakReplies} onChange={e => { setSpeakReplies(e.target.checked); if (!e.target.checked) stopSpeech(); }} /> Spoken replies</label>
          {!s.session && <select aria-label="AI connection" value={s.credentialId} onChange={e => s.setCredentialId(e.target.value)}><option value="">Platform AI</option>{s.credentials.map(key => <option key={key.id} value={key.id}>{key.name}</option>)}</select>}
        </div>
      </form>
    </section>
  </main>;
}
