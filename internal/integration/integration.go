package integration

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
)

// ErrInvalid reports invalid integration metadata.
var ErrInvalid = errors.New("integration: invalid")

// idPattern is the shape of integration and action IDs: lower case snake
// case. Dots are reserved as the separator of the node type.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Auth is what an integration needs to authenticate. Required false means
// the actions take no credential at all.
type Auth struct {
	Required bool
	// Provider is the credential provider (credential.Credential.Provider).
	// Several integrations can share one: Gmail and Google Docs would both
	// use "google".
	Provider string
	// CredentialType is the accepted kind of credential (API_KEY, OAUTH2...).
	CredentialType credential.Type
}

// Integration describes one third-party service. It is static, code-defined
// metadata: nothing about integrations is stored in the database.
type Integration struct {
	ID          string // "gmail"
	Name        string // "Gmail"
	Description string
	// Category groups integrations in the UI ("Google", "Messaging").
	Category string
	// Icon is an optional icon identifier for the frontend.
	Icon string
	// DocsURL optionally points at help for setting the integration up.
	DocsURL string
	Auth    Auth
	Actions []Action
}

// Action is one capability of an integration, exposed as one workflow node.
type Action struct {
	ID          string // "send"
	Name        string // "Send email"
	Description string
	// SideEffects classifies what re-running the action does (Phase 10):
	// node.SideEffectsNone for reads, SideEffectsIdempotent when the action
	// deduplicates with NodeInput.IdempotencyKey, SideEffectsUnsafe
	// otherwise (sending a message).
	SideEffects node.SideEffects
	Inputs      []node.PortDefinition
	Outputs     []node.PortDefinition
	// Config lists the action's own settings. The credential_id field is
	// added by Definition when the integration needs authentication.
	Config []node.ConfigField
}

// NodeType is the node type of an integration action: "<integration>.<action>".
func NodeType(integrationID, actionID string) string {
	return integrationID + "." + actionID
}

// SplitNodeType returns the integration and action of an integration node
// type; ok is false for built-in node types (which contain no dot).
func SplitNodeType(nodeType string) (integrationID, actionID string, ok bool) {
	i, a, found := strings.Cut(nodeType, ".")
	if !found || !idPattern.MatchString(i) || !idPattern.MatchString(a) {
		return "", "", false
	}
	return i, a, true
}

// forbiddenConfig are config field names that would put a secret into a
// workflow definition. Secrets belong in credentials, referenced by ID.
var forbiddenConfig = map[string]bool{
	"api_key": true, "apikey": true, "access_token": true, "refresh_token": true, "id_token": true,
	"token": true, "secret": true, "client_secret": true, "password": true, "authorization": true,
	"authorization_code": true, "private_key": true,
}

// IsSecretConfigKey reports whether a config key looks like an inline secret.
func IsSecretConfigKey(key string) bool {
	return forbiddenConfig[strings.ReplaceAll(strings.ToLower(key), "-", "_")]
}

// Validate checks the integration's metadata and every action's.
func (in Integration) Validate() error {
	switch {
	case !idPattern.MatchString(in.ID):
		return fmt.Errorf("%w: integration id %q must match %s", ErrInvalid, in.ID, idPattern)
	case strings.TrimSpace(in.Name) == "":
		return fmt.Errorf("%w: integration %q needs a name", ErrInvalid, in.ID)
	case len(in.Actions) == 0:
		return fmt.Errorf("%w: integration %q has no actions", ErrInvalid, in.ID)
	}
	if in.Auth.Required {
		if strings.TrimSpace(in.Auth.Provider) == "" {
			return fmt.Errorf("%w: integration %q requires auth but names no credential provider", ErrInvalid, in.ID)
		}
		if !in.Auth.CredentialType.Valid() {
			return fmt.Errorf("%w: integration %q has unknown credential type %q", ErrInvalid, in.ID, in.Auth.CredentialType)
		}
	}
	seen := map[string]bool{}
	for _, a := range in.Actions {
		if !idPattern.MatchString(a.ID) {
			return fmt.Errorf("%w: action id %q of %q must match %s", ErrInvalid, a.ID, in.ID, idPattern)
		}
		if seen[a.ID] {
			return fmt.Errorf("%w: duplicate action %q in integration %q", ErrInvalid, a.ID, in.ID)
		}
		seen[a.ID] = true
		for _, f := range a.Config {
			if f.Name == node.CredentialConfigField {
				return fmt.Errorf("%w: action %q declares %q itself; it is added from the integration's auth", ErrInvalid, NodeType(in.ID, a.ID), f.Name)
			}
			if IsSecretConfigKey(f.Name) {
				return fmt.Errorf("%w: action %q must not have a %q config field: secrets belong in credentials", ErrInvalid, NodeType(in.ID, a.ID), f.Name)
			}
		}
		if err := in.Definition(a).Validate(); err != nil {
			return fmt.Errorf("%w: action %q: %v", ErrInvalid, NodeType(in.ID, a.ID), err)
		}
	}
	return nil
}

// Action returns the action with the given ID.
func (in Integration) Action(id string) (Action, bool) {
	for _, a := range in.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// Definition is the node definition of one of the integration's actions:
// the ordinary metadata the node registry, the graph validator and
// GET /api/v1/nodes already work with.
func (in Integration) Definition(a Action) node.NodeDefinition {
	def := node.NodeDefinition{
		Type:        NodeType(in.ID, a.ID),
		Name:        in.Name + ": " + a.Name,
		Description: a.Description,
		Category:    node.CategoryIntegration,
		Inputs:      append([]node.PortDefinition{}, a.Inputs...),
		Outputs:     append([]node.PortDefinition{}, a.Outputs...),
		SideEffects: a.SideEffects,
		Integration: &node.IntegrationRef{ID: in.ID, Name: in.Name, Action: a.ID, Category: in.Category, Icon: in.Icon, DocsURL: in.DocsURL},
	}
	if in.Auth.Required {
		def.Auth = &node.AuthRequirement{Required: true, Provider: in.Auth.Provider, CredentialType: string(in.Auth.CredentialType)}
		def.Config = append(def.Config, node.NewConfigField(node.CredentialConfigField, node.ValueTypeString, true, "",
			"The "+in.Name+" account (workspace credential) to use.").WithLabel("Account"))
	}
	def.Config = append(def.Config, a.Config...)
	return def
}

// Definitions returns the node definitions of all actions, in order.
func (in Integration) Definitions() []node.NodeDefinition {
	out := make([]node.NodeDefinition, 0, len(in.Actions))
	for _, a := range in.Actions {
		out = append(out, in.Definition(a))
	}
	return out
}
