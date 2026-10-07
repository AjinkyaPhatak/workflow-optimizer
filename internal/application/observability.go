package application

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/workspace"
)

// ObservabilityPolicy decides which persisted node payloads the debugger may
// show. Events never contain payloads regardless of the policy; this only
// governs the node inputs/outputs the engine already records (Phase 8).
// Whatever is shown is redacted (credentials, tokens, keys) and truncated.
type ObservabilityPolicy struct {
	ExposeNodeInputs  bool
	ExposeNodeOutputs bool
}

// DefaultObservabilityPolicy shows redacted node inputs and outputs to
// workspace members (the same members who can already read the execution's
// own input and output).
func DefaultObservabilityPolicy() ObservabilityPolicy {
	return ObservabilityPolicy{ExposeNodeInputs: true, ExposeNodeOutputs: true}
}

// EventReader reads execution events.
type EventReader interface {
	ListByExecution(ctx context.Context, executionID uuid.UUID, q execution.EventQuery) ([]execution.ExecutionEvent, int, error)
}

// ExecutionLister lists a workflow's executions.
type ExecutionLister interface {
	ListByWorkflow(ctx context.Context, workflowID uuid.UUID, limit, offset int) ([]execution.Execution, int, error)
}

// TokenUsage is token consumption reported by a provider.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// ExecutionDetails is an execution with values derived from its node
// records. Nothing here is stored twice: aggregates are computed on read.
type ExecutionDetails struct {
	Execution execution.Execution
	// Duration from the first claim to completion (nil until finished).
	Duration *time.Duration
	// NodeCount is the number of distinct workflow nodes that ran.
	NodeCount int
	// Retries counts execution retries (attempts after the first) and
	// in-place node retries.
	Retries int
	// Usage sums the token usage providers reported (nil when none did).
	Usage *TokenUsage
	// EstimatedCost sums the per-node estimates (nil when none exists).
	EstimatedCost *float64
}

// NodeExecutionDetails is a node record prepared for inspection.
type NodeExecutionDetails struct {
	Record execution.NodeExecution
	// Input / Output are redacted copies (nil when hidden by policy).
	Input, Output map[string]any
	Duration      *time.Duration
	// Provider / Model / Usage are derived from what the node recorded
	// (its config and its "model" / "usage" outputs); nil when absent.
	Provider, Model *string
	Usage           *TokenUsage
	EstimatedCost   *float64
}

// ObservabilityService reads executions for the debugger. It only reads:
// it never changes execution state.
type ObservabilityService struct {
	executions *ExecutionService
	events     EventReader
	lister     ExecutionLister
	pricing    *observability.Pricing
	policy     ObservabilityPolicy
}

// NewObservabilityService wires the service. Authorization reuses the
// execution service's workspace check.
func NewObservabilityService(executions *ExecutionService, events EventReader, lister ExecutionLister, pricing *observability.Pricing, policy ObservabilityPolicy) *ObservabilityService {
	return &ObservabilityService{executions: executions, events: events, lister: lister, pricing: pricing, policy: policy}
}

// GetExecution returns the execution and its aggregates.
func (s *ObservabilityService) GetExecution(ctx context.Context, user, id uuid.UUID) (ExecutionDetails, error) {
	e, err := s.executions.execution(ctx, user, id, workspace.ActionRead)
	if err != nil {
		return ExecutionDetails{}, err
	}
	records, err := s.executions.nodes.ListByExecution(ctx, id)
	if err != nil {
		return ExecutionDetails{}, err
	}
	d := ExecutionDetails{Execution: e, Retries: max(e.Attempt-1, 0)}
	if e.StartedAt != nil && e.FinishedAt != nil {
		dur := e.FinishedAt.Sub(*e.StartedAt)
		d.Duration = &dur
	}
	distinct := map[string]bool{}
	perInvocationGroup := map[string]int{}
	for _, n := range records {
		distinct[n.NodeID] = true
		perInvocationGroup[n.NodeID+"#"+strconv.Itoa(n.ExecutionAttempt)]++
		nd := s.describe(n)
		if nd.Usage != nil {
			if d.Usage == nil {
				d.Usage = &TokenUsage{}
			}
			d.Usage.InputTokens += nd.Usage.InputTokens
			d.Usage.OutputTokens += nd.Usage.OutputTokens
			d.Usage.TotalTokens += nd.Usage.TotalTokens
		}
		if nd.EstimatedCost != nil {
			if d.EstimatedCost == nil {
				d.EstimatedCost = new(float64)
			}
			*d.EstimatedCost += *nd.EstimatedCost
		}
	}
	for _, c := range perInvocationGroup {
		d.Retries += c - 1
	}
	d.NodeCount = len(distinct)
	return d, nil
}

// GetNodeExecutions returns the execution's node records in execution order.
func (s *ObservabilityService) GetNodeExecutions(ctx context.Context, user, id uuid.UUID) ([]NodeExecutionDetails, error) {
	if _, err := s.executions.execution(ctx, user, id, workspace.ActionRead); err != nil {
		return nil, err
	}
	records, err := s.executions.nodes.ListByExecution(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]NodeExecutionDetails, 0, len(records))
	for _, n := range records {
		out = append(out, s.describe(n))
	}
	return out, nil
}

// GetEvents returns one page of the execution's events, oldest first.
func (s *ObservabilityService) GetEvents(ctx context.Context, user, id uuid.UUID, q execution.EventQuery) ([]execution.ExecutionEvent, int, error) {
	if _, err := s.executions.execution(ctx, user, id, workspace.ActionRead); err != nil {
		return nil, 0, err
	}
	events, total, err := s.events.ListByExecution(ctx, id, q)
	if err != nil {
		return nil, 0, err
	}
	for i := range events {
		events[i].Data = observability.RedactMap(events[i].Data)
	}
	return events, total, nil
}

// ListExecutions returns one page of a workflow's executions (newest first).
func (s *ObservabilityService) ListExecutions(ctx context.Context, user, workflowID uuid.UUID, page Page) ([]execution.Execution, int, error) {
	if _, _, err := s.executions.workflows.workflow(ctx, user, workflowID, workspace.ActionRead); err != nil {
		return nil, 0, err
	}
	return s.lister.ListByWorkflow(ctx, workflowID, page.Size, page.offset())
}

func (s *ObservabilityService) describe(n execution.NodeExecution) NodeExecutionDetails {
	d := NodeExecutionDetails{Record: n}
	if n.StartedAt != nil && n.FinishedAt != nil {
		dur := n.FinishedAt.Sub(*n.StartedAt)
		d.Duration = &dur
	}
	if s.policy.ExposeNodeInputs {
		d.Input = observability.RedactMap(n.Input)
	}
	if s.policy.ExposeNodeOutputs {
		d.Output = observability.RedactMap(n.Output)
	}
	config, _ := n.Input["config"].(map[string]any)
	if p, ok := config["provider"].(string); ok && p != "" {
		d.Provider = &p
	}
	if m, ok := n.Output["model"].(string); ok && m != "" {
		d.Model = &m
	} else if m, ok := config["model"].(string); ok && m != "" && d.Provider != nil {
		d.Model = &m
	}
	if u, ok := n.Output["usage"].(map[string]any); ok {
		in, okIn := number(u["input_tokens"])
		outT, okOut := number(u["output_tokens"])
		total, okTotal := number(u["total_tokens"])
		if okIn || okOut || okTotal {
			if !okTotal {
				total = in + outT
			}
			d.Usage = &TokenUsage{InputTokens: in, OutputTokens: outT, TotalTokens: total}
			if d.Model != nil {
				d.EstimatedCost = s.pricing.Estimate(*d.Model, in, outT)
			}
		}
	}
	return d
}

func number(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
