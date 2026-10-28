package planning

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
)

func TestGenericLiveQueryRetriesEmptySourcesWithoutADataQuestion(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(map[bool]string{true: "weather_records", false: "no_source"}[found], func(t *testing.T) {
			calls := 0
			client := New("synthetic-key", "", "gpt-6-luna")
			client.SDK.Responses.Options = append(client.SDK.Responses.Options, option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request["tool_choice"] != "required" || request["tools"].([]any)[0].(map[string]any)["type"] != "web_search" {
					t.Fatal("live search was optional or absent")
				}
				instructions := request["instructions"].(string)
				if !strings.Contains(instructions, "Application clock at request:") || !strings.Contains(instructions, "Missing source publication/quote timestamps do not block usable records") {
					t.Fatal("query lacks the application's retrieval clock policy")
				}
				rows := [][]string{}
				if calls == 2 && found {
					rows = [][]string{{"Pune", "31", "https://example.test/weather"}}
				}
				text, _ := json.Marshal(map[string]any{"columns": []string{"city", "temperature_c", "source_url"}, "rows": rows, "summary": "Weather findings", "notes": "Available source coverage"})
				body, _ := json.Marshal(map[string]any{"id": "synthetic", "object": "response", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(text), "annotations": []any{}}}}}, "usage": map[string]int{"total_tokens": 3}})
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}))
			result, err := client.Query(context.Background(), "Current weather in Pune", "Return city, temperature_c and source_url")
			if err != nil || calls != 2 {
				t.Fatalf("query did not recover with bounded search: calls=%d err=%v", calls, err)
			}
			rows := result.Outputs["rows"].([]map[string]any)
			if len(rows) != map[bool]int{true: 1, false: 0}[found] || result.Tokens != 6 {
				t.Fatalf("bad rows or usage: %+v", result)
			}
			if found {
				if rows[0]["city"] != "Pune" {
					t.Fatal("query was tied to a particular dataset")
				}
				if _, err := time.Parse(time.RFC3339, rows[0]["observed_at"].(string)); err != nil {
					t.Fatal("retrieval time missing")
				}
			}
		})
	}
}
