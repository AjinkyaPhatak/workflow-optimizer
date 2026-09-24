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
