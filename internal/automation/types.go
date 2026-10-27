// Package automation defines the versioned, provider-independent workflow format.
// The legacy workflow/execution packages remain responsible for v1 runs.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = 2

var ErrNotFound = errors.New("automation: not found")
var ErrConflict = errors.New("automation: revision changed")
var ErrUnavailable = errors.New("automation: service not configured")

type Binding struct {
	Name      string `json:"name"`
	Source    string `json:"source"` // literal, step, answer, item, event
	Ref       string `json:"ref"`
	ValueJSON string `json:"value_json"`
}
type Condition struct {
	Left  Binding `json:"left"`
	Op    string  `json:"op"`
	Right Binding `json:"right"`
}
type Option struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type Question struct {
	ID         string   `json:"id"`
	Prompt     string   `json:"prompt"`
	Kind       string   `json:"kind"`
	Required   bool     `json:"required"`
	AllowOther bool     `json:"allow_other"`
	Options    []Option `json:"options"`
	Example    string   `json:"example"`
	Min        *float64 `json:"min"`
	Max        *float64 `json:"max"`
	StepID     string   `json:"step_id"`
	Input      string   `json:"input"`
}
type Step struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Tool            string     `json:"tool"`
	Inputs          []Binding  `json:"inputs"`
	Children        []Step     `json:"children"`
	Otherwise       []Step     `json:"otherwise"`
	Condition       *Condition `json:"condition"`
	Question        *Question  `json:"question"`
	MaxIterations   int        `json:"max_iterations"`
	DurationSeconds int        `json:"duration_seconds"`
	WorkflowRef     string     `json:"workflow_ref"`
}
type Budget struct {
	MaxActions       int `json:"max_actions"`
	MaxTokens        int `json:"max_tokens"`
	MaxActiveSeconds int `json:"max_active_seconds"`
}

func (b Budget) Valid() bool { return b.MaxActions > 0 && b.MaxTokens > 0 && b.MaxActiveSeconds > 0 }

type Plan struct {
	SchemaVersion int        `json:"schema_version"`
	Name          string     `json:"name"`
	Summary       string     `json:"summary"`
	Mode          string     `json:"mode"` // once or continuous
	Budget        Budget     `json:"budget"`
	Steps         []Step     `json:"steps"`
	Questions     []Question `json:"questions"`
}
type Field struct {
	Label       string   `json:"label,omitempty"`
	Default     any      `json:"default,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Multiline   bool     `json:"multiline,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Options     []Option `json:"options,omitempty"`
}
type ToolDescriptor struct {
	Category      string         `json:"category,omitempty"`
	Icon          string         `json:"icon,omitempty"`
	IntegrationID string         `json:"integration_id,omitempty"`
	InputSchema   map[string]any `json:"input_schema"`
	OutputSchema  map[string]any `json:"output_schema"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Inputs        []Field        `json:"inputs"`
	Outputs       []Field        `json:"outputs"`
	SideEffects   string         `json:"side_effects"`
	Provider      string         `json:"provider,omitempty"`
}
type Diagnostic struct {
	StepID     string `json:"step_id"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RepairHint string `json:"repair_hint,omitempty"`
}
type Compilation struct {
	Plan        Plan         `json:"plan"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Ready       bool         `json:"ready"`
}
type ArtifactRef struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}
type Message struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}
type Session struct {
	ID           uuid.UUID      `json:"id"`
	WorkspaceID  uuid.UUID      `json:"workspace_id"`
	UserID       uuid.UUID      `json:"user_id"`
	Revision     int            `json:"revision"`
	CredentialID uuid.UUID      `json:"credential_id"`
	Messages     []Message      `json:"messages"`
	Compilation  *Compilation   `json:"compilation"`
	Answers      map[string]any `json:"answers"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}
type Run struct {
	Ancestors    []uuid.UUID               `json:"ancestors,omitempty"`
	Trace        []uuid.UUID               `json:"trace"`
	ID           uuid.UUID                 `json:"id"`
	WorkspaceID  uuid.UUID                 `json:"workspace_id"`
	UserID       uuid.UUID                 `json:"user_id"`
	SessionID    *uuid.UUID                `json:"session_id"`
	Plan         Plan                      `json:"plan"`
	Revision     int                       `json:"revision"`
	CredentialID uuid.UUID                 `json:"credential_id"`
	Backend      string                    `json:"execution_backend"`
	Status       string                    `json:"status"`
	Outputs      map[string]map[string]any `json:"outputs"`
	Question     *Question                 `json:"question"`
	Error        string                    `json:"error"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
}
type Event struct {
	ID        int64     `json:"id"`
	RunID     uuid.UUID `json:"run_id"`
	Kind      string    `json:"kind"`
	StepID    string    `json:"step_id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
type Automation struct {
	Source       string    `json:"source"`
	ID           uuid.UUID `json:"id"`
	WorkspaceID  uuid.UUID `json:"workspace_id"`
	UserID       uuid.UUID `json:"user_id"`
	Name         string    `json:"name"`
	Plan         Plan      `json:"plan"`
	CredentialID uuid.UUID `json:"credential_id"`
	Cron         string    `json:"cron"`
	Timezone     string    `json:"timezone"`
	Trigger      string    `json:"trigger"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}
type Backend interface {
	Start(context.Context, Run, map[string]any, any) error
	Answer(context.Context, uuid.UUID, string, int, any) error
	Control(context.Context, uuid.UUID, string, *Budget) error
	Event(context.Context, uuid.UUID, any) error
	Schedule(context.Context, Automation) error
	Trigger(context.Context, Automation, string, any) error
}
type ToolCall struct {
	TokenLimit  int
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	RunID       uuid.UUID
	OperationID string
	Tool        string
	Inputs      map[string]any
}
type ToolResult struct {
	Outputs map[string]any `json:"outputs"`
	Tokens  int            `json:"tokens"`
}
type ToolExecutor interface {
	Execute(context.Context, ToolCall) (ToolResult, error)
}
type ValueResolver interface {
	Hydrate(context.Context, ToolCall, any) (any, error)
}
type NeedInput struct{ Question Question }

func (e *NeedInput) Error() string { return e.Question.Prompt }

type Store interface {
	CreateSession(context.Context, Session) error
	Session(context.Context, uuid.UUID) (Session, error)
	SaveSession(context.Context, Session, int) error
	ListSessions(context.Context, uuid.UUID) ([]Session, error)
	CreateRun(context.Context, Run) error
	Run(context.Context, uuid.UUID) (Run, error)
	ListRuns(context.Context, uuid.UUID) ([]Run, error)
	UpdateRun(context.Context, uuid.UUID, string, map[string]map[string]any, *Question, string) error
	AddEvent(context.Context, Event) error
	Events(context.Context, uuid.UUID, int64) ([]Event, error)
	ClaimAction(context.Context, ToolCall) (bool, *ToolResult, error)
	FinishAction(context.Context, ToolCall, ToolResult, string) error
	SaveAutomation(context.Context, Automation) error
	DeleteAutomation(context.Context, uuid.UUID) error
	Automation(context.Context, uuid.UUID) (Automation, error)
	ListAutomations(context.Context, uuid.UUID) ([]Automation, error)
	AcceptTrigger(context.Context, uuid.UUID, string) (bool, error)
}

func ValidateAnswer(q Question, value any) error {
	if value == nil || value == "" {
		if q.Required {
			return fmt.Errorf("please answer %s", q.Prompt)
		}
		return nil
	}
	switch q.Kind {
	case "number":
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("enter a number")
		}
		if q.Min != nil && n < *q.Min {
			return fmt.Errorf("enter at least %v", *q.Min)
		}
		if q.Max != nil && n > *q.Max {
			return fmt.Errorf("enter at most %v", *q.Max)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("choose yes or no")
		}
	case "choice", "contact", "file":
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errors.New("choose an option")
		}
		for _, o := range q.Options {
			if o.Value == s {
				return nil
			}
		}
		if !q.AllowOther {
			return errors.New("choose one of the listed options")
		}
	case "date":
		s, ok := value.(string)
		if !ok {
			return errors.New("enter a date")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return errors.New("enter a date as YYYY-MM-DD")
		}
	case "text":
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errors.New("enter some text")
		}
		if len(s) > 20000 {
			return errors.New("the answer is too long")
		}
	default:
		return fmt.Errorf("unsupported question type %q", q.Kind)
	}
	if s, ok := value.(string); ok && HasPlaceholder(s) {
		return errors.New("replace the example placeholder with the real value")
	}
	return nil
}
func Resolve(b Binding, outputs map[string]map[string]any, answers map[string]any, item, event any) (any, error) {
	var value any
	var path string
	switch b.Source {
	case "literal":
		if err := json.Unmarshal([]byte(b.ValueJSON), &value); err != nil {
			return nil, fmt.Errorf("%s needs a valid value", b.Name)
		}
	case "answer":
		var ok bool
		value, ok = answers[b.Ref]
		if !ok {
			return nil, fmt.Errorf("answer %s is missing", b.Ref)
		}
	case "template":
		var text string
		if err := json.Unmarshal([]byte(b.ValueJSON), &text); err != nil {
			return nil, err
		}
		index := 0
		var missing string
		text = placeholder.ReplaceAllStringFunc(text, func(token string) string {
			id := fmt.Sprintf("%s_%d", b.Ref, index)
			index++
			v, ok := answers[id]
			if !ok {
				missing = id
				return token
			}
			return fmt.Sprint(v)
		})
		if missing != "" {
			return nil, fmt.Errorf("answer %s is missing", missing)
		}
		value = text
	case "step":
		root, rest, _ := strings.Cut(b.Ref, ".")
		value = outputs[root]
		path = rest
		if value == nil {
			return nil, fmt.Errorf("step %s has not produced a result", root)
		}
	case "item":
		value, path = item, b.Ref
	case "event":
		value, path = event, b.Ref
	default:
		return nil, fmt.Errorf("unknown input source %s", b.Source)
	}
	parts := strings.Split(path, ".")
	for index, part := range parts {
		if part == "" {
			continue
		}
		switch v := value.(type) {
		case map[string]any:
			if _, ok := v["$artifact"]; ok {
				ref := map[string]any{}
				for k, val := range v {
					ref[k] = val
				}
				prefix, _ := v["$path"].(string)
				suffix := strings.Join(parts[index:], ".")
				if prefix != "" {
					suffix = prefix + "." + path
				}
				ref["$path"] = suffix
				return ref, nil
			}
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, fmt.Errorf("field %s was not returned", part)
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil, fmt.Errorf("item %s does not exist", part)
			}
			value = v[i]
		default:
			return nil, fmt.Errorf("cannot read field %s", part)
		}
	}
	return value, nil
}
func Evaluate(c Condition, outputs map[string]map[string]any, answers map[string]any, item, event any) (bool, error) {
	l, err := Resolve(c.Left, outputs, answers, item, event)
	if err != nil {
		return false, err
	}
	r, err := Resolve(c.Right, outputs, answers, item, event)
	if err != nil {
		return false, err
	}
	return CompareValues(c.Op, l, r)
}
func CompareValues(op string, l, r any) (bool, error) {
	op, _ = CanonicalComparison(op)
	switch op {
	case "equals":
		a, _ := json.Marshal(l)
		b, _ := json.Marshal(r)
		return string(a) == string(b), nil
	case "not_equals":
		a, _ := json.Marshal(l)
		b, _ := json.Marshal(r)
		return string(a) != string(b), nil
	case "contains":
		return strings.Contains(fmt.Sprint(l), fmt.Sprint(r)), nil
	case "greater", "less", "at_least", "at_most":
		a, aok := l.(float64)
		b, bok := r.(float64)
		if !aok || !bok {
			return false, errors.New("comparison needs two numbers")
		}
		switch op {
		case "greater":
			return a > b, nil
		case "less":
			return a < b, nil
		case "at_least":
			return a >= b, nil
		default:
			return a <= b, nil
		}
	default:
		return false, fmt.Errorf("unknown comparison %s", op)
	}
}
