// Package app manages application-level lifecycle, dependency composition,
// and startup bootstrapping for the Workflow Optimizer system.
package app

import (
	"fmt"
	"net/http"

	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/condition"
	nodehttp "workflow-optimizer/internal/node/http"
	"workflow-optimizer/internal/node/input"
	nodejson "workflow-optimizer/internal/node/json"
	"workflow-optimizer/internal/node/llm"
	"workflow-optimizer/internal/node/merge"
	"workflow-optimizer/internal/node/output"
	"workflow-optimizer/internal/node/prompt"
	"workflow-optimizer/internal/node/structured_output"
	"workflow-optimizer/internal/node/text"
	"workflow-optimizer/internal/node/transform"
	providerllm "workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/provider/llm/openai"
)

// Application encapsulates the runtime registries and configuration of the system.
type Application struct {
	Config           config.Config
	NodeRegistry     node.Registry
	ProviderRegistry providerllm.Registry
}

// Dependencies are the runtime collaborators Bootstrap cannot create without
// I/O (Phase 11).
type Dependencies struct {
	// Credentials resolves workspace credentials for provider-backed nodes
	// (credential.Service in production). Nil leaves the LLM node unwired:
	// it then answers with its Phase 4 stub and never calls a provider.
	Credentials credential.Resolver
	// HTTPClient is the shared client of the HTTP-based providers (nil: the
	// provider's default, TLS-verifying client).
	HTTPClient *http.Client
}

// Bootstrap composes the application without runtime dependencies (no
// credential resolution). See BootstrapWith.
func Bootstrap(cfg config.Config) (*Application, error) {
	return BootstrapWith(cfg, Dependencies{})
}

// BootstrapWith is the single startup composition point. It creates the
// provider registry (OpenAI registered, then frozen) and the node registry,
// constructs all canonical V1 nodes with their dependencies, registers them
// with their metadata definitions, and validates registry consistency:
//
//	credential resolver + provider registry -> LLM node -> node registry
//
// The executor sees only the node registry; it knows nothing about providers.
func BootstrapWith(cfg config.Config, deps Dependencies) (*Application, error) {
	// 1. Construct Provider Registry
	providerReg := providerllm.NewRegistry()
	openAI, err := openai.New(openai.Options{BaseURL: cfg.OpenAIBaseURL, HTTPClient: deps.HTTPClient})
	if err != nil {
		return nil, fmt.Errorf("bootstrap openai provider: %w", err)
	}
	if err := providerReg.Register(openai.Name, openAI); err != nil {
		return nil, fmt.Errorf("bootstrap register provider %q: %w", openai.Name, err)
	}
	providerReg.Freeze()
	llmDeps := llm.Dependencies{}
	if deps.Credentials != nil {
		llmDeps = llm.Dependencies{Providers: providerReg, Credentials: deps.Credentials}
	}

	// 2. Construct Node Registry
	nodeReg := node.NewRegistry()

	// 3. Construct all V1 nodes with explicit dependencies injected outside the registry
	type nodePair struct {
		node node.Node
		def  node.NodeDefinition
	}

	nodesToRegister := []nodePair{
		// General category
		{node: input.New(), def: input.Definition()},
		{node: output.New(), def: output.Definition()},
		{node: text.New(), def: text.Definition()},
		{node: nodejson.New(), def: nodejson.Definition()},
		{node: transform.New(), def: transform.Definition()},
		{node: condition.New(), def: condition.Definition()},
		{node: merge.New(), def: merge.Definition()},

		// AI category
		{node: prompt.New(), def: prompt.Definition()},
		{node: llm.New(llmDeps), def: llm.DefinitionFor(providerllm.Describe(providerReg))},
		{node: structured_output.New(nil), def: structured_output.Definition()},

		// Integration category
		{node: nodehttp.New(), def: nodehttp.Definition()},
	}

	// 4. Register nodes and definitions
	for _, p := range nodesToRegister {
		if err := nodeReg.RegisterNode(p.node, p.def); err != nil {
			return nil, fmt.Errorf("bootstrap register node %q: %w", p.node.Type(), err)
		}
	}

	// 5. Validate registry-wide consistency
	if err := nodeReg.Validate(); err != nil {
		return nil, fmt.Errorf("bootstrap registry validation failed: %w", err)
	}

	return &Application{
		Config:           cfg,
		NodeRegistry:     nodeReg,
		ProviderRegistry: providerReg,
	}, nil
}
