package node

import "fmt"

// Category classifies node definitions for UI grouping, cataloging, and discovery.
type Category string

const (
	CategoryGeneral     Category = "general"
	CategoryAI          Category = "ai"
	CategoryLLM         Category = "llm"
	CategoryIntegration Category = "integration"

	// Extensible future categories
	CategoryKnowledgeBase  Category = "knowledge_base"
	CategoryMultiModal     Category = "multimodal"
	CategoryHumanInTheLoop Category = "human_in_the_loop"
	CategoryEvaluation     Category = "evaluation"
	CategoryUtilities      Category = "utilities"
)

// SemanticRole describes a graph-boundary capability. Category is UI metadata;
// roles are used by static graph validation.
type SemanticRole string

const (
	SemanticRoleNone  SemanticRole = ""
	SemanticRoleEntry SemanticRole = "entry"
	SemanticRoleExit  SemanticRole = "exit"
)

// SideEffects declares what re-running a node can do to the outside world.
// The retry engine consults it before re-running a node (Phase 10).
type SideEffects string

const (
	// SideEffectsNone: the node only computes; re-running it is always safe.
	SideEffectsNone SideEffects = ""
	// SideEffectsIdempotent: the node changes external state but deduplicates
	// repeated calls with NodeInput.IdempotencyKey (e.g. by sending it as an
	// Idempotency-Key header), so a retry does not repeat the effect when the
	// target honours the key.
	SideEffectsIdempotent SideEffects = "idempotent"
	// SideEffectsUnsafe: the node changes external state and cannot
	// deduplicate (emails, payments, non-idempotent writes). A failed attempt
	// is never re-run automatically unless its error states NotApplied.
	SideEffectsUnsafe SideEffects = "unsafe"
)

// Valid reports whether s is a known side-effect declaration.
func (s SideEffects) Valid() bool {
	return s == SideEffectsNone || s == SideEffectsIdempotent || s == SideEffectsUnsafe
}

// NodeDefinition is the static metadata describing what a node type is capable of:
// its identity, declared input/output ports, and configuration schema.
// It is strictly decoupled from workflow.Node (which represents a configured instance
// in a workflow graph) and from node.Node (which is the executable implementation).
type NodeDefinition struct {
	Type        string           `json:"type"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Category    Category         `json:"category,omitempty"`
	Role        SemanticRole     `json:"role,omitempty"`
	Inputs      []PortDefinition `json:"inputs"`
	Outputs     []PortDefinition `json:"outputs"`
	Config      []ConfigField    `json:"config,omitempty"`
	// SideEffects declares whether re-running the node is safe (Phase 10).
	SideEffects SideEffects `json:"side_effects,omitempty"`
}

// GetInputPort looks up an input port definition by name.
func (d NodeDefinition) GetInputPort(name string) (PortDefinition, bool) {
	for _, p := range d.Inputs {
		if p.Name == name {
			return p, true
		}
	}
	return PortDefinition{}, false
}

// GetOutputPort looks up an output port definition by name.
func (d NodeDefinition) GetOutputPort(name string) (PortDefinition, bool) {
	for _, p := range d.Outputs {
		if p.Name == name {
			return p, true
		}
	}
	return PortDefinition{}, false
}

// GetConfigField looks up a configuration field definition by name.
func (d NodeDefinition) GetConfigField(name string) (ConfigField, bool) {
	for _, f := range d.Config {
		if f.Name == name {
			return f, true
		}
	}
	return ConfigField{}, false
}

// Validate checks the structural integrity of the node definition.
func (d NodeDefinition) Validate() error {
	if d.Type == "" {
		return fmt.Errorf("%w: definition type is required", ErrInvalidDefinition)
	}
	if d.Name == "" {
		return fmt.Errorf("%w: definition name is required", ErrInvalidDefinition)
	}
	if d.Category == "" {
		return fmt.Errorf("%w: definition category is required", ErrInvalidDefinition)
	}
	if !d.SideEffects.Valid() {
		return fmt.Errorf("%w: unknown side effects %q", ErrInvalidDefinition, d.SideEffects)
	}

	seenInputs := make(map[string]struct{}, len(d.Inputs))
	for _, in := range d.Inputs {
		if in.Name == "" {
			return fmt.Errorf("%w: input port name is required", ErrInvalidDefinition)
		}
		if _, exists := seenInputs[in.Name]; exists {
			return fmt.Errorf("%w: duplicate input port %q", ErrInvalidDefinition, in.Name)
		}
		seenInputs[in.Name] = struct{}{}
	}

	seenOutputs := make(map[string]struct{}, len(d.Outputs))
	for _, out := range d.Outputs {
		if out.Name == "" {
			return fmt.Errorf("%w: output port name is required", ErrInvalidDefinition)
		}
		if _, exists := seenOutputs[out.Name]; exists {
			return fmt.Errorf("%w: duplicate output port %q", ErrInvalidDefinition, out.Name)
		}
		seenOutputs[out.Name] = struct{}{}
	}

	seenConfig := make(map[string]struct{}, len(d.Config))
	for _, cfg := range d.Config {
		if cfg.Name == "" {
			return fmt.Errorf("%w: config field name is required", ErrInvalidDefinition)
		}
		if _, exists := seenConfig[cfg.Name]; exists {
			return fmt.Errorf("%w: duplicate config field %q", ErrInvalidDefinition, cfg.Name)
		}
		seenConfig[cfg.Name] = struct{}{}
	}

	return nil
}
