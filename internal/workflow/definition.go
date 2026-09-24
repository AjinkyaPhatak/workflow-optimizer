package workflow

import "regexp"

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
