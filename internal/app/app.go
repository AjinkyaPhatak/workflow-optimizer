// Package app manages application-level lifecycle, dependency composition,
// and startup bootstrapping for the Workflow Optimizer system.
package app

import (
	"fmt"

	"workflow-optimizer/internal/config"
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
)

// Application encapsulates the runtime registries and configuration of the system.
type Application struct {
	Config           config.Config
	NodeRegistry     node.Registry
	ProviderRegistry providerllm.Registry
}

// Bootstrap is the single startup composition point. It creates application
// dependencies, sets up the provider and node registries, constructs all canonical
// V1 nodes with their dependencies, registers them with their metadata definitions,
// and validates registry consistency before returning.
func Bootstrap(cfg config.Config) (*Application, error) {
	// 1. Construct Provider Registry
	providerReg := providerllm.NewRegistry()

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
		{node: llm.New(nil), def: llm.Definition()},
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
