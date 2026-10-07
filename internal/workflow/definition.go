package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// DefinitionSchemaVersion is the supported JSON schema version. It is separate
// from Version.VersionNumber, which identifies a persisted workflow revision.
const DefinitionSchemaVersion = 1

// Definition is the canonical, serializable workflow graph stored in a
// workflow version's definition JSONB field.
type Definition struct {
	Version  int            `json:"version"`
	Nodes    []Node         `json:"nodes"`
	Edges    []Edge         `json:"edges"`
	Settings map[string]any `json:"settings"`
	// Variables are the workflow's declared inputs (Phase B). Optional:
	// definitions without variables serialize exactly as before, so the
	// schema version is unchanged.
	Variables []Variable `json:"variables,omitempty"`
}

// VariableType is the type of a workflow variable's value.
type VariableType string

const (
	VariableString  VariableType = "string"
	VariableNumber  VariableType = "number"
	VariableBoolean VariableType = "boolean"
	VariableObject  VariableType = "object"
	VariableArray   VariableType = "array"
)

// Variable is a user-defined workflow variable. Its value comes from the run
// input field of the same name, or Default when the input has none; nodes
// reference it as {{name}} (or {{input.name}}). A variable without a default
// must be supplied by every run.
type Variable struct {
	Name        string       `json:"name"`
	Type        VariableType `json:"type"`
	Default     any          `json:"default"`
	Description string       `json:"description,omitempty"`
}

var variableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidVariableName reports whether name can be referenced as {{name}}.
func ValidVariableName(name string) bool {
	return variableNamePattern.MatchString(name)
}

// Matches reports whether v is a value of the variable's type.
func (t VariableType) Matches(v any) bool {
	switch t {
	case VariableString:
		_, ok := v.(string)
		return ok
	case VariableNumber:
		switch v.(type) {
		case float64, int, int64, json.Number:
			return true
		}
		return false
	case VariableBoolean:
		_, ok := v.(bool)
		return ok
	case VariableObject:
		_, ok := v.(map[string]any)
		return ok
	case VariableArray:
		_, ok := v.([]any)
		return ok
	}
	return false
}

// Known reports whether t is a supported variable type.
func (t VariableType) Known() bool {
	switch t {
	case VariableString, VariableNumber, VariableBoolean, VariableObject, VariableArray:
		return true
	}
	return false
}

// ApplyVariables returns the run input completed with the variables'
// defaults. It reports, per variable, a value of the wrong type or a missing
// value without a default. The input is not modified.
func (d Definition) ApplyVariables(input map[string]any) (map[string]any, []string) {
	out := make(map[string]any, len(input)+len(d.Variables))
	for k, v := range input {
		out[k] = v
	}
	var problems []string
	for _, v := range d.Variables {
		val, given := input[v.Name]
		switch {
		case given && val != nil:
			if !v.Type.Matches(val) {
				problems = append(problems, fmt.Sprintf("variable %q must be a %s", v.Name, v.Type))
			}
		case v.Default != nil:
			out[v.Name] = v.Default
		default:
			problems = append(problems, fmt.Sprintf("variable %q has no value and no default", v.Name))
		}
	}
	return out, problems
}

// Node is an instance in a workflow graph, not a node-type definition and not
// a database record.
type Node struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Position *Position      `json:"position"`
	Config   map[string]any `json:"config"`
}

// Position is presentation metadata and has no execution semantics.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge connects an output port on one node to an input port on another node.
type Edge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"source_port"`
	Target     string `json:"target"`
	TargetPort string `json:"target_port"`
}

// PortDataType identifies the kind of data a future node-definition port can
// carry. The string form keeps the contract extensible.
type PortDataType string

const (
	PortDataTypeString  PortDataType = "string"
	PortDataTypeNumber  PortDataType = "number"
	PortDataTypeBoolean PortDataType = "boolean"
	PortDataTypeJSON    PortDataType = "json"
	PortDataTypeObject  PortDataType = "object"
	PortDataTypeArray   PortDataType = "array"
	PortDataTypeBinary  PortDataType = "binary"
)

// Port is a future node-definition port declaration. Edges only reference its
// identifier; workflow nodes do not embed port definitions.
type Port struct {
	ID       string       `json:"id"`
	DataType PortDataType `json:"data_type"`
}

// NodeDefinition describes a node type separately from a workflow Node. A
// registry and node-specific configuration schemas are intentionally deferred.
type NodeDefinition struct {
	Type    string `json:"type"`
	Inputs  []Port `json:"inputs"`
	Outputs []Port `json:"outputs"`
}

// VariableReference is a reference expression embedded in configuration text.
// It is declarative; no runtime resolution occurs in this package.
type VariableReference struct {
	Expression string
}

var variableReferencePattern = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_.]*)\}\}`)

// VariableReferences returns all valid {{variable.path}} references in text.
func VariableReferences(text string) []VariableReference {
	matches := variableReferencePattern.FindAllStringSubmatch(text, -1)
	references := make([]VariableReference, 0, len(matches))
	for _, match := range matches {
		references = append(references, VariableReference{Expression: match[1]})
	}
	return references
}
