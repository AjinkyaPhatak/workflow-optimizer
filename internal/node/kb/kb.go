// Package kb holds the knowledge base nodes (Phase D2): Knowledge Search
// (retrieval for prompts) and Add to Knowledge Base (index text from a
// workflow).
package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/knowledge"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/llm"
)

// Node types.
const (
	SearchType = "knowledge_search"
	AddType    = "knowledge_add"
)

func baseField() node.ConfigField {
	return node.NewConfigField("knowledge_base_id", node.ValueTypeString, true, "", "A knowledge base of this workspace (in the Library).").
		WithLabel("Knowledge base").WithOptionsSource("knowledge_bases")
}

// SearchDefinition is the metadata of Knowledge Search.
func SearchDefinition() node.NodeDefinition {
	return node.NodeDefinition{
		Type: SearchType, Name: "Knowledge Search", Category: node.CategoryKnowledgeBase,
		Description: "Finds the passages of a knowledge base closest in meaning to a question, ready to put in an LLM prompt.",
		Inputs:      []node.PortDefinition{node.NewPortDefinition("query", node.ValueTypeJSON, false, "Any upstream data; text is used as the question (overriding the Query setting)")},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("context", node.ValueTypeString, true, "The passages as numbered text: [1] title, passage..."),
			node.NewPortDefinition("results", node.ValueTypeArray, true, "Passages, best first: text, score, document_title, document_id"),
			node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of passages"),
		},
		Config: []node.ConfigField{
			baseField(),
			node.NewConfigField("query", node.ValueTypeString, false, "", "The question. {{variables}} are filled in.").WithLabel("Query"),
			node.NewConfigField("top_k", node.ValueTypeNumber, false, 5.0, "How many passages to return.").WithLabel("Passages").
				WithRange(node.Float(1), node.Float(knowledge.MaxChunksPerSearch), node.Float(1)),
			node.NewConfigField("min_score", node.ValueTypeNumber, false, 0.0, "Leave out passages less similar than this (0 to 1; 0 keeps all).").
				WithLabel("Minimum similarity").WithRange(node.Float(0), node.Float(1), node.Float(0.05)),
		},
	}
}

// AddDefinition is the metadata of Add to Knowledge Base.
func AddDefinition() node.NodeDefinition {
	return node.NodeDefinition{
		Type: AddType, Name: "Add to Knowledge Base", Category: node.CategoryKnowledgeBase,
		Description: "Indexes text (an email, a page, an extracted document) into a knowledge base so later searches find it.",
		// A retried run finds the document the first attempt added.
		SideEffects: node.SideEffectsIdempotent,
		Inputs:      []node.PortDefinition{node.NewPortDefinition("text", node.ValueTypeJSON, false, "Any upstream data; text is indexed (overriding the Text setting)")},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("document_id", node.ValueTypeString, true, "The new document ID"),
			node.NewPortDefinition("chunks", node.ValueTypeNumber, true, "Number of passages indexed"),
		},
		Config: []node.ConfigField{
			baseField(),
			node.NewConfigField("title", node.ValueTypeString, false, "", "Shown with search results. Empty: the first line of the text. {{variables}} are filled in.").WithLabel("Title"),
			node.NewConfigField("text", node.ValueTypeString, false, "", "Used when the text input is not connected. {{variables}} are filled in.").WithLabel("Text").WithMultiline(),
		},
	}
}

// Node is either knowledge node.
type Node struct {
	typ string
	svc *knowledge.Service
}

// NewSearch returns Knowledge Search (svc nil: runs fail with a
// configuration error).
func NewSearch(svc *knowledge.Service) *Node { return &Node{typ: SearchType, svc: svc} }

// NewAdd returns Add to Knowledge Base.
func NewAdd(svc *knowledge.Service) *Node { return &Node{typ: AddType, svc: svc} }

// Type implements node.Node.
func (n *Node) Type() string { return n.typ }

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func textOrPort(in node.NodeInput, port, setting string) string {
	if v, ok := in.GetPort(port); ok {
		if s, ok := v.String(); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	s, _ := in.GetStringConfig(setting)
	return s
}

// classify maps knowledge failures to node errors.
func classify(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, knowledge.ErrNotFound):
		return node.NewNodeError(node.ErrCodeInvalidInput, "the knowledge base was not found in this workspace", false)
	case errors.Is(err, knowledge.ErrInvalid):
		msg := strings.TrimPrefix(err.Error(), knowledge.ErrInvalid.Error()+": ")
		if i := strings.Index(msg, " ("); i > 0 {
			msg = msg[:i] // keep the reason, not the wrapped credential detail
		}
		return node.NewNodeError(node.ErrCodeInvalidInput, msg, false)
	}
	return llm.ClassifyProviderError(err)
}

func (n *Node) base(in node.NodeInput) (uuid.UUID, error) {
	raw, _ := in.GetStringConfig("knowledge_base_id")
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, configError("choose a Knowledge base")
	}
	return id, nil
}

// Execute implements node.Node.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if n.svc == nil {
		return node.NodeOutput{}, configError("knowledge bases are not available")
	}
	baseID, err := n.base(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if n.typ == AddType {
		return n.add(ctx, in, baseID)
	}
	k := 5
	if f, ok := in.GetNumberConfig("top_k"); ok {
		if f < 1 || f > knowledge.MaxChunksPerSearch || f != float64(int(f)) {
			return node.NodeOutput{}, configError(fmt.Sprintf("top_k must be a whole number from 1 to %d", knowledge.MaxChunksPerSearch))
		}
		k = int(f)
	}
	minScore, _ := in.GetNumberConfig("min_score")
	if minScore < 0 || minScore > 1 {
		return node.NodeOutput{}, configError("min_score must be from 0 to 1")
	}
	query := strings.TrimSpace(textOrPort(in, "query", "query"))
	if query == "" {
		return node.NodeOutput{}, configError("set a Query, or connect the question")
	}
	results, err := n.svc.Search(ctx, in.Scope.WorkspaceID, baseID, query, k, minScore)
	if err != nil {
		return node.NodeOutput{}, classify(err)
	}
	list := make([]any, len(results))
	for i, r := range results {
		list[i] = map[string]any{"text": r.Content, "score": r.Score, "document_title": r.DocumentTitle, "document_id": r.DocumentID.String()}
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("context", node.NewStringValue(knowledge.Context(results)))
	out.SetPort("results", node.NewArrayValue(list))
	out.SetPort("count", node.NewNumberValue(float64(len(results))))
	return out, nil
}

func (n *Node) add(ctx context.Context, in node.NodeInput, baseID uuid.UUID) (node.NodeOutput, error) {
	text := textOrPort(in, "text", "text")
	if strings.TrimSpace(text) == "" {
		return node.NodeOutput{}, configError("set the Text, or connect the text to add")
	}
	title, _ := in.GetStringConfig("title")
	add := knowledge.AddInput{WorkspaceID: in.Scope.WorkspaceID, BaseID: baseID, Title: title, Text: text, Source: "text"}
	if in.IdempotencyKey != "" {
		add.ID = knowledge.DocumentID(baseID, in.IdempotencyKey)
	}
	b, d, created, err := n.svc.Begin(ctx, add)
	if err != nil {
		return node.NodeOutput{}, classify(err)
	}
	if created || d.Status != knowledge.StatusReady {
		if d, err = n.svc.Index(ctx, b, d, strings.TrimSpace(text)); err != nil {
			return node.NodeOutput{}, classify(err)
		}
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("document_id", node.NewStringValue(d.ID.String()))
	out.SetPort("chunks", node.NewNumberValue(float64(d.Chunks)))
	return out, nil
}

// OptionSources implements the options provider contract (Phase C8).
func (n *Node) OptionSources() []string { return []string{"knowledge_bases"} }

// Options lists the workspace knowledge bases.
func (n *Node) Options(ctx context.Context, scope node.Scope, source string, _ map[string]any) ([]node.ConfigOption, error) {
	if source != "knowledge_bases" || n.svc == nil {
		return nil, configError("no such choices")
	}
	bases, err := n.svc.Repository().ListBases(ctx, scope.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]node.ConfigOption, len(bases))
	for i, b := range bases {
		out[i] = node.ConfigOption{Value: b.ID.String(), Label: b.Name}
	}
	return out, nil
}
