package integration_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// gmailLike is metadata shaped like a future Gmail integration. It is only
// metadata in a test: nothing here talks to Google.
func gmailLike() integration.Integration {
	return integration.Integration{
		ID: "gmail", Name: "Gmail", Category: "Google", Icon: "gmail",
		Auth: integration.Auth{Required: true, Provider: "google", CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{ID: "search", Name: "Search", SideEffects: node.SideEffectsNone,
				Inputs:  []node.PortDefinition{node.NewPortDefinition("query", node.ValueTypeString, false, "")},
				Outputs: []node.PortDefinition{node.NewPortDefinition("messages", node.ValueTypeArray, true, "")}},
			{ID: "create_draft", Name: "Create draft", SideEffects: node.SideEffectsIdempotent,
				Outputs: []node.PortDefinition{node.NewPortDefinition("draft", node.ValueTypeJSON, true, "")}},
			{ID: "send", Name: "Send", SideEffects: node.SideEffectsUnsafe,
				Inputs: []node.PortDefinition{node.NewPortDefinition("to", node.ValueTypeString, true, "")},
				Config: []node.ConfigField{node.NewConfigField("subject", node.ValueTypeString, false, "", "")}},
		},
	}
}

func TestNodeTypeConvention(t *testing.T) {
	if got := integration.NodeType("gmail", "create_draft"); got != "gmail.create_draft" {
		t.Fatalf("NodeType = %q", got)
	}
	for typ, want := range map[string][2]string{"gmail.send": {"gmail", "send"}, "google_docs.append": {"google_docs", "append"}} {
		i, a, ok := integration.SplitNodeType(typ)
		if !ok || i != want[0] || a != want[1] {
			t.Errorf("SplitNodeType(%q) = %q %q %v", typ, i, a, ok)
		}
	}
	for _, typ := range []string{"llm", "structured_output", "a.b.c", "Gmail.send", ".send", "gmail.", ""} {
		if _, _, ok := integration.SplitNodeType(typ); ok {
			t.Errorf("SplitNodeType(%q) accepted a non-integration type", typ)
		}
	}
}

func TestIntegrationValidation(t *testing.T) {
	if err := gmailLike().Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	cases := map[string]func(*integration.Integration){
		"id with a dot":           func(i *integration.Integration) { i.ID = "test.integration" },
		"upper case id":           func(i *integration.Integration) { i.ID = "Gmail" },
		"no name":                 func(i *integration.Integration) { i.Name = " " },
		"no actions":              func(i *integration.Integration) { i.Actions = nil },
		"auth without provider":   func(i *integration.Integration) { i.Auth.Provider = "" },
		"unknown credential type": func(i *integration.Integration) { i.Auth.CredentialType = "google_oauth" },
		"duplicate action":        func(i *integration.Integration) { i.Actions = append(i.Actions, i.Actions[0]) },
		"bad action id":           func(i *integration.Integration) { i.Actions[0].ID = "send-now" },
		"unknown side effects":    func(i *integration.Integration) { i.Actions[0].SideEffects = "sometimes" },
		"own credential_id field": func(i *integration.Integration) {
			i.Actions[0].Config = []node.ConfigField{node.NewConfigField("credential_id", node.ValueTypeString, false, "", "")}
		},
		"inline access token field": func(i *integration.Integration) {
			i.Actions[0].Config = []node.ConfigField{node.NewConfigField("access_token", node.ValueTypeString, false, "", "")}
		},
		"inline api key field": func(i *integration.Integration) {
			i.Actions[0].Config = []node.ConfigField{node.NewConfigField("API-Key", node.ValueTypeString, false, "", "")}
		},
		"duplicate port": func(i *integration.Integration) {
			i.Actions[0].Inputs = append(i.Actions[0].Inputs, i.Actions[0].Inputs[0])
		},
	}
	for name, mutate := range cases {
		in := gmailLike()
		mutate(&in)
		if err := in.Validate(); !errors.Is(err, integration.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	// No auth: no credential needed, no provider required.
	open := gmailLike()
	open.Auth = integration.Auth{}
	if err := open.Validate(); err != nil {
		t.Fatalf("integration without auth rejected: %v", err)
	}
	if def := open.Definition(open.Actions[0]); def.Auth != nil || len(def.Config) != 0 {
		t.Fatalf("no-auth action must not get auth metadata or a credential field: %+v", def)
	}
}

func TestActionDefinitionMetadata(t *testing.T) {
	in := gmailLike()
	send, _ := in.Action("send")
	def := in.Definition(send)
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	if def.Type != "gmail.send" || def.Name != "Gmail: Send" || def.Category != node.CategoryIntegration {
		t.Fatalf("identity: %+v", def)
	}
	if def.Integration == nil || *def.Integration != (node.IntegrationRef{ID: "gmail", Name: "Gmail", Action: "send", Category: "Google", Icon: "gmail"}) {
		t.Fatalf("integration ref: %+v", def.Integration)
	}
	if def.Auth == nil || *def.Auth != (node.AuthRequirement{Required: true, Provider: "google", CredentialType: "OAUTH2"}) {
		t.Fatalf("auth: %+v", def.Auth)
	}
	// The credential reference is the first, required field; the action's
	// own fields follow. No field can hold a secret.
	if len(def.Config) != 2 || def.Config[0].Name != node.CredentialConfigField || !def.Config[0].Required || def.Config[1].Name != "subject" {
		t.Fatalf("config: %+v", def.Config)
	}
	// Side effects are carried into the node definition the retry engine reads.
	want := map[string]node.SideEffects{"gmail.search": node.SideEffectsNone, "gmail.create_draft": node.SideEffectsIdempotent, "gmail.send": node.SideEffectsUnsafe}
	for _, d := range in.Definitions() {
		if d.SideEffects != want[d.Type] {
			t.Errorf("%s side effects %q, want %q", d.Type, d.SideEffects, want[d.Type])
		}
	}
}

func TestNodeDefinitionRejectsInconsistentIntegrationMetadata(t *testing.T) {
	def := gmailLike().Definition(gmailLike().Actions[2])
	bad := def
	bad.Type = "gmail_send"
	if err := bad.Validate(); !errors.Is(err, node.ErrInvalidDefinition) {
		t.Fatalf("type not matching <integration>.<action> accepted: %v", err)
	}
	bad = def
	bad.Config = bad.Config[1:]
	if err := bad.Validate(); !errors.Is(err, node.ErrInvalidDefinition) {
		t.Fatalf("auth without a credential_id field accepted: %v", err)
	}
	bad = def
	bad.Auth = &node.AuthRequirement{Required: true}
	if err := bad.Validate(); !errors.Is(err, node.ErrInvalidDefinition) {
		t.Fatalf("auth without provider accepted: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	reg := integration.NewRegistry()
	if err := reg.Register(gmailLike()); err != nil {
		t.Fatal(err)
	}
	other := gmailLike()
	other.ID, other.Name = "discord", "Discord"
	other.Auth = integration.Auth{Required: true, Provider: "discord", CredentialType: credential.TypeBearerToken}
	if err := reg.Register(other); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(gmailLike()); !errors.Is(err, integration.ErrDuplicate) {
		t.Fatalf("duplicate integration: %v", err)
	}
	bad := gmailLike()
	bad.ID = "bad.id"
	if err := reg.Register(bad); !errors.Is(err, integration.ErrInvalid) {
		t.Fatalf("invalid integration registered: %v", err)
	}

	if got, err := reg.Get("gmail"); err != nil || got.Name != "Gmail" {
		t.Fatalf("Get: %+v %v", got, err)
	}
	if _, err := reg.Get("notion"); !errors.Is(err, integration.ErrNotFound) {
		t.Fatalf("unknown integration: %v", err)
	}
	in, a, err := reg.Action("gmail.create_draft")
	if err != nil || in.ID != "gmail" || a.ID != "create_draft" || a.SideEffects != node.SideEffectsIdempotent {
		t.Fatalf("Action: %v %v %v", in.ID, a, err)
	}
	for _, typ := range []string{"gmail.delete", "notion.search", "llm"} {
		if _, _, err := reg.Action(typ); !errors.Is(err, integration.ErrNotFound) {
			t.Errorf("Action(%q): %v", typ, err)
		}
	}
	actions, err := reg.Actions("gmail")
	if err != nil || len(actions) != 3 || actions[0].ID != "search" || actions[2].ID != "send" {
		t.Fatalf("Actions: %+v %v", actions, err)
	}
	actions[0].ID = "mutated" // callers get copies
	if again, _ := reg.Actions("gmail"); again[0].ID != "search" {
		t.Fatal("registry state changed through a returned slice")
	}
	if l := reg.List(); len(l) != 2 || l[0].ID != "discord" || l[1].ID != "gmail" {
		t.Fatalf("List not sorted: %v", l)
	}

	reg.Freeze()
	late := gmailLike()
	late.ID = "notion"
	if err := reg.Register(late); !errors.Is(err, integration.ErrFrozen) {
		t.Fatalf("register after freeze: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, _, err := reg.Action("gmail.send"); err != nil {
					t.Error(err)
					return
				}
				_ = reg.List()
			}
		}()
	}
	wg.Wait()
}

type stubNode struct{ typ string }

func (s stubNode) Type() string { return s.typ }
func (s stubNode) Execute(context.Context, node.NodeInput) (node.NodeOutput, error) {
	return node.NewNodeOutput(nil), nil
}

func TestInstallRegistersActionsAsOrdinaryNodes(t *testing.T) {
	nodes := node.NewRegistry()
	reg := integration.NewRegistry()
	m := integration.Module{Integration: gmailLike(), Nodes: map[string]node.Node{
		"search": stubNode{"gmail.search"}, "create_draft": stubNode{"gmail.create_draft"}, "send": stubNode{"gmail.send"},
	}}
	if err := integration.Install(reg, nodes, m); err != nil {
		t.Fatal(err)
	}
	if err := nodes.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nodes.List(), ","); got != "gmail.create_draft,gmail.search,gmail.send" {
		t.Fatalf("node registry: %s", got)
	}
	def, err := nodes.GetDefinition("gmail.send")
	if err != nil || def.SideEffects != node.SideEffectsUnsafe || def.Auth == nil {
		t.Fatalf("definition: %+v %v", def, err)
	}
	if _, err := reg.Get("gmail"); err != nil {
		t.Fatal(err)
	}

	missing := integration.Module{Integration: gmailLike(), Nodes: map[string]node.Node{"search": stubNode{"gmail.search"}}}
	if err := integration.Install(integration.NewRegistry(), node.NewRegistry(), missing); !errors.Is(err, integration.ErrInvalid) {
		t.Fatalf("action without node: %v", err)
	}
	extra := integration.Module{Integration: gmailLike(), Nodes: map[string]node.Node{
		"search": stubNode{"gmail.search"}, "create_draft": stubNode{"gmail.create_draft"}, "send": stubNode{"gmail.send"}, "delete": stubNode{"gmail.delete"},
	}}
	if err := integration.Install(integration.NewRegistry(), node.NewRegistry(), extra); !errors.Is(err, integration.ErrInvalid) {
		t.Fatalf("node for unknown action: %v", err)
	}
	wrong := integration.Module{Integration: gmailLike(), Nodes: map[string]node.Node{
		"search": stubNode{"gmail.search"}, "create_draft": stubNode{"gmail.create_draft"}, "send": stubNode{"gmail.reply"},
	}}
	if err := integration.Install(integration.NewRegistry(), node.NewRegistry(), wrong); !errors.Is(err, node.ErrInconsistentType) {
		t.Fatalf("node whose type is not the action's: %v", err)
	}
	// A node type taken by a built-in node cannot be installed twice.
	if err := integration.Install(integration.NewRegistry(), nodes, m); !errors.Is(err, node.ErrNodeAlreadyRegistered) {
		t.Fatalf("duplicate node type: %v", err)
	}
}
