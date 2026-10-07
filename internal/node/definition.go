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
	// Integration identifies the third-party integration action this node
	// performs (Phase C1); nil for built-in nodes. Descriptive only:
	// execution never reads it.
	Integration *IntegrationRef `json:"integration,omitempty"`
	// Auth declares the workspace credential the node uses (Phase C1); nil
	// when the node does not declare one.
	Auth *AuthRequirement `json:"auth,omitempty"`
}

// IntegrationRef links a node type to the integration action it implements.
// The node type is always "<ID>.<Action>".
type IntegrationRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Action string `json:"action"`
	// Category groups integrations in the UI ("Google").
	Category string `json:"category,omitempty"`
	Icon     string `json:"icon,omitempty"`
	DocsURL  string `json:"docs_url,omitempty"`
}

// AuthRequirement says which workspace credential a node uses. A workflow
// stores only the credential's ID (the CredentialConfigField); the secret is
// resolved at run time through the credential service.
type AuthRequirement struct {
	Required bool `json:"required"`
	// Provider is the provider the credential must belong to
	// (credential.Credential.Provider), e.g. "openai" or "google".
	Provider string `json:"provider"`
	// CredentialType is the accepted credential.Type, e.g. "API_KEY" or
	// "OAUTH2".
	CredentialType string `json:"credential_type"`
}

// CredentialConfigField is the config field holding a credential reference.
const CredentialConfigField = "credential_id"

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

	if d.Integration != nil {
		if d.Integration.ID == "" || d.Integration.Action == "" || d.Type != d.Integration.ID+"."+d.Integration.Action {
			return fmt.Errorf("%w: integration node type %q must be <integration>.<action>", ErrInvalidDefinition, d.Type)
		}
	}
	if d.Auth != nil {
		if d.Auth.Provider == "" || d.Auth.CredentialType == "" {
			return fmt.Errorf("%w: auth requirement of %q needs a provider and a credential type", ErrInvalidDefinition, d.Type)
		}
		if _, ok := d.GetConfigField(CredentialConfigField); !ok {
			return fmt.Errorf("%w: %q declares auth but has no %q config field", ErrInvalidDefinition, d.Type, CredentialConfigField)
		}
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
