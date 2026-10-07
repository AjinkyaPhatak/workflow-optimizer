package templates

import "testing"

func TestRemapFollowsRenamedNodes(t *testing.T) {
	ids := map[string]string{"llm": "llm_ab12", "input": "input_cd34"}
	got := remap(map[string]any{
		"text": "{{llm.response}} / {{llm.usage.total_tokens}} / {{input.query}} / {{audience}} / {{other.x}}",
		"list": []any{"{{llm.model}}", 3.0},
	}, ids).(map[string]any)
	if got["text"] != "{{llm_ab12.response}} / {{llm_ab12.usage.total_tokens}} / {{input.query}} / {{audience}} / {{other.x}}" {
		t.Fatalf("text: %q", got["text"])
	}
	if l := got["list"].([]any); l[0] != "{{llm_ab12.model}}" || l[1] != 3.0 {
		t.Fatalf("list: %#v", l)
	}
}
