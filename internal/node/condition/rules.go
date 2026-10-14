package condition

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Operators a rule can use.
var Operators = []struct{ ID, Label string }{
	{"equals", "equals"}, {"not_equals", "does not equal"},
	{"contains", "contains"}, {"not_contains", "does not contain"},
	{"starts_with", "starts with"}, {"ends_with", "ends with"},
	{"greater_than", "is greater than"}, {"less_than", "is less than"},
	{"greater_or_equal", "is greater than or equal to"}, {"less_or_equal", "is less than or equal to"},
	{"is_empty", "is empty"}, {"is_not_empty", "is not empty"},
	{"is_true", "is true"}, {"is_false", "is false"},
	{"matches", "matches the pattern (regex)"},
}

// unary operators ignore the compared value.
var unary = map[string]bool{"is_empty": true, "is_not_empty": true, "is_true": true, "is_false": true}

// Rule is one comparison.
type Rule struct {
	Value         any    `json:"value"`
	Operator      string `json:"operator"`
	Compare       any    `json:"compare"`
	CaseSensitive bool   `json:"case_sensitive"`
}

// text renders a value for text comparisons (JSON for objects and lists).
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, !math.IsNaN(t)
	case bool:
		return 0, false
	default:
		f, err := strconv.ParseFloat(strings.TrimSpace(text(v)), 64)
		return f, err == nil && !math.IsNaN(f)
	}
}

func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func truthy(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case float64:
		return t != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1", "on":
			return true, true
		case "false", "no", "0", "off", "":
			return false, true
		}
	case nil:
		return false, true
	case []any, map[string]any:
		return !empty(v), true // a list or object: true when not empty
	}
	return false, false
}

// Evaluate applies a rule. Numbers compare numerically when both sides are
// numbers (equals included: "5" equals 5.0); text comparisons ignore case
// unless CaseSensitive; contains also finds an item in a list.
func Evaluate(r Rule) (bool, error) {
	op := r.Operator
	if op == "" {
		op = "equals"
	}
	switch op {
	case "is_empty":
		return empty(r.Value), nil
	case "is_not_empty":
		return !empty(r.Value), nil
	case "is_true", "is_false":
		b, ok := truthy(r.Value)
		if !ok {
			return false, fmt.Errorf("%q is not true or false", text(r.Value))
		}
		return b == (op == "is_true"), nil
	}
	fold := func(s string) string {
		if r.CaseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	left, right := fold(text(r.Value)), fold(text(r.Compare))
	switch op {
	case "equals", "not_equals":
		eq := left == right
		if a, ok := number(r.Value); ok {
			if b, ok := number(r.Compare); ok {
				eq = a == b
			}
		}
		return eq == (op == "equals"), nil
	case "contains", "not_contains":
		found := strings.Contains(left, right)
		if list, ok := r.Value.([]any); ok {
			found = false
			for _, item := range list {
				found = found || fold(text(item)) == right
			}
		}
		return found == (op == "contains"), nil
	case "starts_with":
		return strings.HasPrefix(left, right), nil
	case "ends_with":
		return strings.HasSuffix(left, right), nil
	case "greater_than", "less_than", "greater_or_equal", "less_or_equal":
		a, okA := number(r.Value)
		b, okB := number(r.Compare)
		if !okA || !okB {
			return false, fmt.Errorf("%q and %q must both be numbers to compare", text(r.Value), text(r.Compare))
		}
		switch op {
		case "greater_than":
			return a > b, nil
		case "less_than":
			return a < b, nil
		case "greater_or_equal":
			return a >= b, nil
		}
		return a <= b, nil
	case "matches":
		pattern := text(r.Compare)
		if !r.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("the pattern is not a valid regular expression")
		}
		return re.MatchString(text(r.Value)), nil
	}
	return false, fmt.Errorf("unknown operator %q", op)
}
