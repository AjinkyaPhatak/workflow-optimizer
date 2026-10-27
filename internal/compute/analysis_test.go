package compute

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// These tests only use synthetic data in the real network-disabled sandbox.
// Set TEST_ANALYSIS_IMAGE to an already-built analysis image to enable them.
func analysisTestImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("TEST_ANALYSIS_IMAGE")
	if image == "" {
		t.Skip("TEST_ANALYSIS_IMAGE is not set")
	}
	return image
}

func TestAnalyzeSandboxDatasetBindings(t *testing.T) {
	image := analysisTestImage(t)
	rows := []map[string]any{
		{"symbol": "SYNTHETIC", "open": 1, "close": nil},
		{"symbol": "SYNTHETIC", "open": 2, "close": 4},
	}
	result, err := Analyze(context.Background(), image, rows, `import json
assert rows[0]["close"] is None
assert df["symbol"].tolist() == ["SYNTHETIC", "SYNTHETIC"]
assert df["open"].sum() == 3
with open("raw.json", "w") as output:
    json.dump(rows, output)
print("raw rows and DataFrame are available")`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "raw rows and DataFrame are available" {
		t.Fatalf("unexpected analysis text: %q", result.Text)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "raw.json" {
		t.Fatalf("unexpected artifacts: %#v", result.Artifacts)
	}
	data, err := base64.StdEncoding.DecodeString(result.Artifacts[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["close"] != nil || got[1]["close"] != float64(4) {
		t.Fatalf("raw JSON values did not survive the sandbox: %s", data)
	}
}

func TestAnalyzeSandboxDefaultStatistics(t *testing.T) {
	image := analysisTestImage(t)
	result, err := Analyze(context.Background(), image, []map[string]any{
		{"value": 1}, {"value": 3},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "| mean | 2 |") {
		t.Fatalf("default statistics missing expected synthetic mean: %q", result.Text)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "statistics.csv" {
		t.Fatalf("unexpected default artifacts: %#v", result.Artifacts)
	}
}

func TestAnalyzeSandboxCodeFailure(t *testing.T) {
	image := analysisTestImage(t)
	_, err := Analyze(context.Background(), image, []any{}, `raise RuntimeError("synthetic failure")`)
	if err == nil || !strings.Contains(err.Error(), "isolated analysis could not finish") {
		t.Fatalf("expected isolated analysis failure, got %v", err)
	}
}
