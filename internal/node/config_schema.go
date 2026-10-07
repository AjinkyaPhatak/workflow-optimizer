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

	// Label is the human-readable field name ("" lets clients derive one).
	Label string `json:"label,omitempty"`
	// Options lists the values the field accepts. With AllowCustom false the
	// graph validator rejects any other (non-reference) value; with
	// AllowCustom true the options are suggestions.
	Options     []ConfigOption `json:"options,omitempty"`
	AllowCustom bool           `json:"allow_custom,omitempty"`
	// Min, Max and Step bound a number field (nil: unbounded).
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Step *float64 `json:"step,omitempty"`
	// Multiline marks long free text (prompts, templates).
	Multiline bool `json:"multiline,omitempty"`
}

// ConfigOption is one accepted value of a ConfigField.
type ConfigOption struct {
	Value any    `json:"value"`
	Label string `json:"label,omitempty"`
	// When restricts the option to configurations where each named field
	// has the given value (e.g. a model offered only for its provider).
	When map[string]any `json:"when,omitempty"`
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

// WithLabel returns the field with a display label.
func (f ConfigField) WithLabel(label string) ConfigField {
	f.Label = label
	return f
}

// WithOptions returns the field restricted to the given options.
func (f ConfigField) WithOptions(options ...ConfigOption) ConfigField {
	f.Options = options
	return f
}

// WithCustom returns the field with its options as suggestions only.
func (f ConfigField) WithCustom() ConfigField {
	f.AllowCustom = true
	return f
}

// WithRange returns the number field bounded to [min, max] in steps of step
// (a nil bound is open).
func (f ConfigField) WithRange(min, max, step *float64) ConfigField {
	f.Min, f.Max, f.Step = min, max, step
	return f
}

// WithMultiline returns the field marked as long free text.
func (f ConfigField) WithMultiline() ConfigField {
	f.Multiline = true
	return f
}

// Float returns a pointer to v (for WithRange).
func Float(v float64) *float64 { return &v }

// Option builds a ConfigOption.
func Option(value any, label string) ConfigOption {
	return ConfigOption{Value: value, Label: label}
}

// Allows reports whether v is an accepted value given the node's whole
// configuration cfg (for When conditions). Fields without options accept
// everything; so do fields allowing custom values.
func (f ConfigField) Allows(v any, cfg map[string]any) bool {
	if len(f.Options) == 0 || f.AllowCustom {
		return true
	}
	for _, o := range f.Options {
		if o.Value == v && o.applies(cfg) {
			return true
		}
	}
	return false
}

func (o ConfigOption) applies(cfg map[string]any) bool {
	for k, want := range o.When {
		if cfg[k] != want {
			return false
		}
	}
	return true
}

// InRange reports whether a number is within the field's bounds.
func (f ConfigField) InRange(v float64) bool {
	return (f.Min == nil || v >= *f.Min) && (f.Max == nil || v <= *f.Max)
}
