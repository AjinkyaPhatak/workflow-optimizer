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

func TestFreshIDsAreReferenceSafeForIntegrationTypes(t *testing.T) {
	taken := map[string]bool{}
	id := freshID("gmail.create_draft", taken)
	if !reference.MatchString("{{"+id+".draft}}") || reference.FindStringSubmatch("{{" + id + ".draft}}")[1] != id {
		t.Fatalf("node ID %q cannot be referenced as {{%s.port}}", id, id)
	}
	if len(id) != len("gmail_create_draft_0000") || id[:len("gmail_create_draft_")] != "gmail_create_draft_" {
		t.Fatalf("id %q", id)
	}
}
