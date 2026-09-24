package node

// PortDefinition declares the specification of an input or output port for a node type.
// Port declarations are reusable for both input and output ports and remain distinct
// from runtime Value payloads.
type PortDefinition struct {
	Name     string    `json:"name"`
	Type     ValueType `json:"type"`
	Required bool      `json:"required"`
	// Multiple declares that an input may receive more than one edge. It has no
	// effect on output ports. The zero value deliberately keeps ordinary inputs
	// single-valued.
	Multiple    bool   `json:"multiple,omitempty"`
	Description string `json:"description,omitempty"`
}

// NewMultiPortDefinition constructs an input port that accepts multiple edges.
func NewMultiPortDefinition(name string, valType ValueType, required bool, description string) PortDefinition {
	p := NewPortDefinition(name, valType, required, description)
	p.Multiple = true
	return p
}

// NewPortDefinition constructs an explicit PortDefinition.
func NewPortDefinition(name string, valType ValueType, required bool, description string) PortDefinition {
	return PortDefinition{
		Name:        name,
		Type:        valType,
		Required:    required,
		Description: description,
	}
}
