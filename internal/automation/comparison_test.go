package automation

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNumericComparisonsAcceptStructuredFetchCells(t *testing.T) {
	for _, tc := range []struct {
		name, op    string
		left, right any
		want        bool
		invalid     bool
	}{
		{"below", "greater", "80784.8", float64(80800), false, false},
		{"above", "greater", "80894", float64(80800), true, false},
		{"threshold", "greater", "80800", float64(80800), false, false},
		{"inclusive", "at_least", "80800", float64(80800), true, false},
		{"json number", "less", json.Number("2.5"), "3", true, false},
		{"currency not guessed", "greater", "$80,894", float64(80800), false, true},
		{"missing", "greater", "", float64(80800), false, true},
		{"nan", "greater", "NaN", float64(80800), false, true},
		{"infinite", "greater", math.Inf(1), float64(80800), false, true},
		{"boolean", "greater", true, float64(80800), false, true},
		{"identifier equality unchanged", "equals", "001", float64(1), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompareValues(tc.op, tc.left, tc.right)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("comparison=%v error=%v", got, err)
			}
		})
	}
}
