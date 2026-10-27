package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/automation"
)

func (h *Handlers) assistantReady(w http.ResponseWriter, r *http.Request) bool {
	if h.Assistant == nil {
		h.fail(w, r, application.ErrUnavailable)
		return false
	}
	return true
}
func (h *Handlers) AssistantTools(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	var ws uuid.UUID
	if r.URL.Query().Get("workspace_id") != "" {
		var ok bool
		ws, ok = h.queryID(w, r, "workspace_id")
		if !ok {
			return
		}
	}
	items, err := h.Assistant.Catalog(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	choices, err := h.Assistant.WorkflowChoices(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"workflows": choices, "items": items, "comparisons": automation.ComparisonOptions(), "controls": automation.ControlDescriptors(), "conversions": automation.ConversionRules})
}
func (h *Handlers) ImportLegacyWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	var in struct {
		WorkflowID   uuid.UUID `json:"workflow_id"`
		VersionID    uuid.UUID `json:"version_id"`
		CredentialID uuid.UUID `json:"credential_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	ctx, cancel := beginAssistantPlanning(w, r)
	defer cancel()
	session, err := h.Assistant.ImportLegacy(ctx, user(r), in.WorkflowID, in.VersionID, in.CredentialID, h.Workflows)
	if err = assistantPlanningError(ctx, err); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, session)
}
func (h *Handlers) CreateAssistantSession(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	var in struct {
		WorkspaceID  uuid.UUID `json:"workspace_id"`
		CredentialID uuid.UUID `json:"credential_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.Create(r.Context(), user(r), in.WorkspaceID, in.CredentialID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, out)
}
func (h *Handlers) ListAssistantSessions(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	out, err := h.Assistant.List(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (h *Handlers) GetAssistantSession(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	out, err := h.Assistant.Get(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) AssistantMessage(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision int    `json:"revision"`
		Text     string `json:"text"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	ctx, cancel := beginAssistantPlanning(w, r)
	defer cancel()
	out, err := h.Assistant.Message(ctx, user(r), id, in.Revision, in.Text)
	if err = assistantPlanningError(ctx, err); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) EditAssistantPlan(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision int             `json:"revision"`
		Plan     automation.Plan `json:"plan"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.Edit(r.Context(), user(r), id, in.Revision, in.Plan)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (h *Handlers) RepairAssistantPlan(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	ctx, cancel := beginAssistantPlanning(w, r)
	defer cancel()
	out, err := h.Assistant.Repair(ctx, user(r), id, in.Revision)
	if err = assistantPlanningError(ctx, err); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) AnswerAssistantDraft(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision   int    `json:"revision"`
		QuestionID string `json:"question_id"`
		Value      any    `json:"value"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	// Intent answers can invoke the planner, just like a new message or repair.
	ctx, cancel := beginAssistantPlanning(w, r)
	defer cancel()
	out, err := h.Assistant.AnswerDraft(ctx, user(r), id, in.Revision, in.QuestionID, in.Value)
	if err = assistantPlanningError(ctx, err); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) StartAssistantRun(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision  int    `json:"revision"`
		RequestID string `json:"request_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.Start(r.Context(), user(r), id, in.Revision, in.RequestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 202, out)
}
func (h *Handlers) ListAutomationRuns(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	out, err := h.Assistant.Runs(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (h *Handlers) GetAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "runID")
	if !ok {
		return
	}
	out, err := h.Assistant.Run(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) AnswerAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "runID")
	if !ok {
		return
	}
	var in struct {
		Revision   int    `json:"revision"`
		QuestionID string `json:"question_id"`
		Value      any    `json:"value"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	if err := h.Assistant.AnswerRun(r.Context(), user(r), id, in.QuestionID, in.Revision, in.Value); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handlers) ControlAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "runID")
	if !ok {
		return
	}
	var in struct {
		Action string             `json:"action"`
		Budget *automation.Budget `json:"budget"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	if err := h.Assistant.Control(r.Context(), user(r), id, in.Action, in.Budget); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handlers) SendAutomationEvent(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "runID")
	if !ok {
		return
	}
	var in struct {
		Event any `json:"event"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	run, err := h.Assistant.Run(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if run.Status != "WAITING_FOR_EVENT" {
		h.fail(w, r, httpx.BadRequest("This workflow is not waiting for an event"))
		return
	}
	if err := h.Assistant.SendEvent(r.Context(), user(r), id, in.Event); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handlers) StreamAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "runID")
	if !ok {
		return
	}
	if _, err := h.Assistant.Run(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		after, _ = strconv.ParseInt(last, 10, 64)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	send := func(kind string, id int64, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if id > 0 {
			if _, err = fmt.Fprintf(w, "id: %d\n", id); err != nil {
				return err
			}
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	var lastUpdate time.Time
	for {
		run, err := h.Assistant.Run(r.Context(), user(r), id)
		if err != nil {
			return
		}
		events, err := h.Assistant.Events(r.Context(), user(r), id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			if send("progress", event.ID, event) != nil {
				return
			}
			after = event.ID
		}
		if !run.UpdatedAt.Equal(lastUpdate) {
			if send("run", 0, run) != nil {
				return
			}
			lastUpdate = run.UpdatedAt
		}
		if run.Status == "COMPLETED" || run.Status == "FAILED" || run.Status == "CANCELLED" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if http.NewResponseController(w).Flush() != nil {
				return
			}
		}
	}
}
func (h *Handlers) ListAutomations(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	out, err := h.Assistant.Automations(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (h *Handlers) SaveAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	var in automation.Automation
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.SaveAutomation(r.Context(), user(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) DeleteAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "automationID")
	if !ok {
		return
	}
	if err := h.Assistant.DeleteAutomation(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handlers) AutomationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	if h.WebhookSecret == "" {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	id, ok := h.pathID(w, r, "automationID")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		h.fail(w, r, httpx.BadRequest("Event is too large"))
		return
	}
	timestamp := r.Header.Get("X-Workflow-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(seconds, 0)).Abs() > 5*time.Minute {
		h.fail(w, r, httpx.Unauthenticated("Webhook timestamp is invalid"))
		return
	}
	mac := hmac.New(sha256.New, []byte(h.WebhookSecret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	signature, err := hex.DecodeString(r.Header.Get("X-Workflow-Signature"))
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		h.fail(w, r, httpx.Unauthenticated("Webhook signature is invalid"))
		return
	}
	var in struct {
		EventID string `json:"event_id"`
		Data    any    `json:"data"`
	}
	if json.Unmarshal(body, &in) != nil {
		h.fail(w, r, httpx.BadRequest("Event must be JSON"))
		return
	}
	config, err := h.Assistant.Store.Automation(r.Context(), id)
	if err != nil || config.Trigger != "webhook" {
		h.fail(w, r, &httpx.Error{Status: 404, Code: "NOT_FOUND", Message: "Trigger not found"})
		return
	}
	if err := h.Assistant.Trigger(r.Context(), id, in.EventID, in.Data); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(202)
}
func (h *Handlers) VoiceToken(w http.ResponseWriter, r *http.Request) {
	if h.Voice == nil {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	var in struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Voice.Token(r.Context(), user(r), in.WorkspaceID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) VoiceSpeak(w http.ResponseWriter, r *http.Request) {
	if h.Voice == nil {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	var in struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		Text        string    `json:"text"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	audio, err := h.Voice.Speak(r.Context(), user(r), in.WorkspaceID, in.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer audio.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, audio)
}
func (h *Handlers) WhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	if h.WhatsApp == nil {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	out, err := h.WhatsApp.Status(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) WhatsAppConnect(w http.ResponseWriter, r *http.Request) {
	if h.WhatsApp == nil {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	var in struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.WhatsApp.Connect(r.Context(), user(r), in.WorkspaceID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 202, out)
}
func (h *Handlers) WhatsAppDisconnect(w http.ResponseWriter, r *http.Request) {
	if h.WhatsApp == nil {
		h.fail(w, r, application.ErrUnavailable)
		return
	}
	var in struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	if err := h.WhatsApp.Disconnect(r.Context(), user(r), in.WorkspaceID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
