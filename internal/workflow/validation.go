// Package workflow provides workflow definition validation and related utilities.
package workflow

import (
	"fmt"
	"strings"
)

// ValidationError identifies a structural problem at a workflow-definition
// path. Semantic checks for ports and node-specific config belong to later work.
type DefinitionValidationError = ValidationError

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// ValidationErrors aggregates all structural problems found in one pass.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, len(e))
	for i, problem := range e {
		parts[i] = problem.Error()
	}
	return strings.Join(parts, "; ")
}

// NodeDefinitionProvider abstracts a source of node definitions for validation.
// It is used by graph validation to ensure only known node types are referenced.
type NodeDefinitionProvider interface {
	HasNodeType(nodeType string) bool
}

// Validate checks the canonical workflow JSON contract and confirms that the
// graph is a directed acyclic graph. It does not execute or schedule nodes.
func (d Definition) Validate() error {
	return d.ValidateWithRegistry(nil)
}

// ValidateWithRegistry validates a workflow definition against an optional
// NodeDefinitionProvider. If the provider is nil, only structural checks are
// performed. When provided, the validator ensures that each node type referenced
// in the workflow has a corresponding definition.
func (d Definition) ValidateWithRegistry(provider NodeDefinitionProvider) error {
	problems := ValidationErrors{}
	if d.Version != DefinitionSchemaVersion {
		problems = append(problems, ValidationError{Path: "version", Message: fmt.Sprintf("unsupported schema version %d", d.Version)})
	}
	if d.Nodes == nil {
		problems = append(problems, ValidationError{Path: "nodes", Message: "is required"})
	}
	if d.Edges == nil {
		problems = append(problems, ValidationError{Path: "edges", Message: "is required"})
	}
	if d.Settings == nil {
		problems = append(problems, ValidationError{Path: "settings", Message: "is required"})
	}

	nodeIDs := make(map[string]struct{}, len(d.Nodes))
	for i, node := range d.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)
		if node.ID == "" {
			problems = append(problems, ValidationError{Path: path + ".id", Message: "is required"})
		} else if _, exists := nodeIDs[node.ID]; exists {
			problems = append(problems, ValidationError{Path: path + ".id", Message: "duplicates a node ID"})
		} else {
			nodeIDs[node.ID] = struct{}{}
		}
		if node.Type == "" {
			problems = append(problems, ValidationError{Path: path + ".type", Message: "is required"})
		}
		if node.Name == "" {
			problems = append(problems, ValidationError{Path: path + ".name", Message: "is required"})
		}
		if node.Position == nil {
			problems = append(problems, ValidationError{Path: path + ".position", Message: "is required"})
		}
		if node.Config == nil {
			problems = append(problems, ValidationError{Path: path + ".config", Message: "is required"})
		} else {
			validateReferences(node.Config, path+".config", &problems)
		}
		if provider != nil && !provider.HasNodeType(node.Type) {
			problems = append(problems, ValidationError{Path: path + ".type", Message: "unknown node type"})
		}
	}
	if d.Settings != nil {
		validateReferences(d.Settings, "settings", &problems)
	}

	edgeIDs := make(map[string]struct{}, len(d.Edges))
	adjacency := make(map[string][]string, len(d.Nodes))
	for nodeID := range nodeIDs {
		adjacency[nodeID] = nil
	}
	for i, edge := range d.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		if edge.ID == "" {
			problems = append(problems, ValidationError{Path: path + ".id", Message: "is required"})
		} else if _, exists := edgeIDs[edge.ID]; exists {
			problems = append(problems, ValidationError{Path: path + ".id", Message: "duplicates an edge ID"})
		} else {
			edgeIDs[edge.ID] = struct{}{}
		}
		if edge.Source == "" {
			problems = append(problems, ValidationError{Path: path + ".source", Message: "is required"})
		} else if _, exists := nodeIDs[edge.Source]; !exists {
			problems = append(problems, ValidationError{Path: path + ".source", Message: "references an unknown node"})
		}
		if edge.Target == "" {
			problems = append(problems, ValidationError{Path: path + ".target", Message: "is required"})
		} else if _, exists := nodeIDs[edge.Target]; !exists {
			problems = append(problems, ValidationError{Path: path + ".target", Message: "references an unknown node"})
		}
		if edge.SourcePort == "" {
			problems = append(problems, ValidationError{Path: path + ".source_port", Message: "is required"})
		}
		if edge.TargetPort == "" {
			problems = append(problems, ValidationError{Path: path + ".target_port", Message: "is required"})
		}
		if _, sourceExists := nodeIDs[edge.Source]; sourceExists {
			if _, targetExists := nodeIDs[edge.Target]; targetExists {
				adjacency[edge.Source] = append(adjacency[edge.Source], edge.Target)
			}
		}
	}
	if hasCycle(adjacency) {
		problems = append(problems, ValidationError{Path: "edges", Message: "must form a directed acyclic graph"})
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}

func validateReferences(value any, path string, problems *ValidationErrors) {
	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, "{{") && len(VariableReferences(typed)) == 0 {
			*problems = append(*problems, ValidationError{Path: path, Message: "contains an invalid variable reference"})
		}
	case map[string]any:
		for key, nested := range typed {
			validateReferences(nested, path+"."+key, problems)
		}
	case []any:
		for i, nested := range typed {
			validateReferences(nested, fmt.Sprintf("%s[%d]", path, i), problems)
		}
	}
}

func hasCycle(adjacency map[string][]string) bool {
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(adjacency))
	var visit func(string) bool
	visit = func(nodeID string) bool {
		states[nodeID] = visiting
		for _, targetID := range adjacency[nodeID] {
			switch states[targetID] {
			case visiting:
				return true
			case unvisited:
				if visit(targetID) {
					return true
				}
			}
		}
		states[nodeID] = visited
		return false
	}
	for nodeID := range adjacency {
		if states[nodeID] == unvisited && visit(nodeID) {
			return true
		}
	}
	return false
}
