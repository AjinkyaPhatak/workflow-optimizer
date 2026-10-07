package responses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

func TestNodeTypeExposesIntegrationMetadata(t *testing.T) {
	in := integration.Integration{
		ID: "notion", Name: "Notion", Icon: "notion", DocsURL: "https://example.test/help",
		Auth: integration.Auth{Required: true, Provider: "notion", CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{{ID: "search", Name: "Search", SideEffects: node.SideEffectsNone,
			Outputs: []node.PortDefinition{node.NewPortDefinition("pages", node.ValueTypeArray, true, "")}}},
	}
	b, _ := json.Marshal(responses.NewNodeType(in.Definition(in.Actions[0])))
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["type"] != "notion.search" || got["category"] != "integration" || got["side_effects"] != "none" {
		t.Fatalf("node type: %s", b)
	}
	if i := got["integration"].(map[string]any); i["id"] != "notion" || i["name"] != "Notion" || i["action"] != "search" || i["icon"] != "notion" || i["docs_url"] != "https://example.test/help" {
		t.Fatalf("integration: %v", i)
	}
	if a := got["auth"].(map[string]any); a["required"] != true || a["provider"] != "notion" || a["credential_type"] != "OAUTH2" {
		t.Fatalf("auth: %v", a)
	}

	// Built-in nodes are unchanged: no integration or auth keys.
	b, _ = json.Marshal(responses.NewNodeType(node.NodeDefinition{Type: "text", Name: "Text", Category: node.CategoryGeneral}))
	if strings.Contains(string(b), "integration") || strings.Contains(string(b), "auth") {
		t.Fatalf("built-in node type changed: %s", b)
	}
}
