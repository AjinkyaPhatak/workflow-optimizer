package node

// ConfigField describes one configuration parameter that a node type accepts.
// It provides metadata for frontend configuration UI, validation, node discovery,
// and documentation without requiring a heavyweight schema system.
type ConfigField struct {
	Name        string    `json:"name"`
	Type        ValueType `json:"type"`
	Required    bool      `json:"required"`
	Default     any       `json:"default,omitempty"`
	Description string    `json:"description,omitempty"`
}

// NewConfigField constructs a ConfigField specification.
func NewConfigField(name string, valType ValueType, required bool, defaultValue any, description string) ConfigField {
	return ConfigField{
		Name:        name,
		Type:        valType,
		Required:    required,
		Default:     defaultValue,
		Description: description,
	}
}
