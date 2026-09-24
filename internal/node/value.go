package node

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ValueType identifies the data type of a port value or configuration field.
// It is represented as a string to allow extensible future types (e.g., image,
// audio, video, document, embedding, message, tool_call) without modifying core contracts.
type ValueType string

const (
	ValueTypeString  ValueType = "string"
	ValueTypeNumber  ValueType = "number"
	ValueTypeBoolean ValueType = "boolean"
	ValueTypeObject  ValueType = "object"
	ValueTypeArray   ValueType = "array"
	ValueTypeJSON    ValueType = "json"
	ValueTypeBinary  ValueType = "binary"

	// Extensible future value types declared as constants for forward compatibility.
	ValueTypeImage     ValueType = "image"
	ValueTypeAudio     ValueType = "audio"
	ValueTypeVideo     ValueType = "video"
	ValueTypeDocument  ValueType = "document"
	ValueTypeEmbedding ValueType = "embedding"
	ValueTypeMessage   ValueType = "message"
	ValueTypeToolCall  ValueType = "tool_call"
)

// Value represents runtime data moving between node ports.
// It associates a concrete payload with its ValueType.
type Value struct {
	Type ValueType `json:"type"`
	Data any       `json:"data"`
}

// NewValue constructs a generic Value with the specified type and data.
func NewValue(t ValueType, data any) Value {
	return Value{
		Type: t,
		Data: data,
	}
}

// NewStringValue creates a Value with ValueTypeString.
func NewStringValue(s string) Value {
	return Value{Type: ValueTypeString, Data: s}
}

// NewNumberValue creates a Value with ValueTypeNumber.
func NewNumberValue(n float64) Value {
	return Value{Type: ValueTypeNumber, Data: n}
}

// NewBooleanValue creates a Value with ValueTypeBoolean.
func NewBooleanValue(b bool) Value {
	return Value{Type: ValueTypeBoolean, Data: b}
}

// NewObjectValue creates a Value with ValueTypeObject.
func NewObjectValue(m map[string]any) Value {
	return Value{Type: ValueTypeObject, Data: m}
}

// NewArrayValue creates a Value with ValueTypeArray.
func NewArrayValue(a []any) Value {
	return Value{Type: ValueTypeArray, Data: a}
}

// NewJSONValue creates a Value with ValueTypeJSON.
func NewJSONValue(data any) Value {
	return Value{Type: ValueTypeJSON, Data: data}
}

// NewBinaryValue creates a Value with ValueTypeBinary.
func NewBinaryValue(b []byte) Value {
	return Value{Type: ValueTypeBinary, Data: b}
}

// String returns the string representation if the value is of type string.
func (v Value) String() (string, bool) {
	if v.Data == nil {
		return "", false
	}
	s, ok := v.Data.(string)
	return s, ok
}

// Number returns the float64 representation if the value is a number or numeric type.
func (v Value) Number() (float64, bool) {
	switch n := v.Data.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// Boolean returns the bool value if the underlying data is a boolean.
func (v Value) Boolean() (bool, bool) {
	if v.Data == nil {
		return false, false
	}
	b, ok := v.Data.(bool)
	return b, ok
}

// Object returns map[string]any if the value represents an object.
func (v Value) Object() (map[string]any, bool) {
	if v.Data == nil {
		return nil, false
	}
	m, ok := v.Data.(map[string]any)
	return m, ok
}

// Array returns []any if the value represents an array.
func (v Value) Array() ([]any, bool) {
	if v.Data == nil {
		return nil, false
	}
	a, ok := v.Data.([]any)
	return a, ok
}

// Binary returns []byte if the value represents binary data (handling []byte or base64 string).
func (v Value) Binary() ([]byte, bool) {
	switch b := v.Data.(type) {
	case []byte:
		return b, true
	case string:
		decoded, err := base64.StdEncoding.DecodeString(b)
		if err == nil {
			return decoded, true
		}
		return []byte(b), true
	default:
		return nil, false
	}
}

// IsZero returns true if the Value has no type and nil data.
func (v Value) IsZero() bool {
	return v.Type == "" && v.Data == nil
}

// GoString implements fmt.GoStringer for readable test assertions.
func (v Value) GoString() string {
	return fmt.Sprintf("node.Value{Type: %q, Data: %#v}", v.Type, v.Data)
}
