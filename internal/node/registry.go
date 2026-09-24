package node

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is the canonical application-level registry mapping workflow node types
// to executable Node implementations and static metadata definitions.
// It is thread-safe for concurrent read access during workflow execution.
type Registry interface {
	// Executable node operations
	Register(node Node) error
	Get(nodeType string) (Node, error)
	Resolve(nodeType string) (Node, bool)
	List() []string

	// Metadata definition operations
	RegisterDefinition(def NodeDefinition) error
	GetDefinition(nodeType string) (NodeDefinition, error)
	Definitions() []NodeDefinition

	// Atomic registration and consistency validation
	RegisterNode(node Node, def NodeDefinition) error
	Validate() error

	// Query helpers
	HasNodeType(nodeType string) bool
}

// NodeRegistry is an alias for Registry, representing the canonical registry contract.
type NodeRegistry = Registry

// DefinitionRegistry stores and provides static metadata definitions for node types.
// It allows discovery, validation, and documentation inspection independently from
// executable node instances.
type DefinitionRegistry interface {
	Register(def NodeDefinition) error
	Get(nodeType string) (NodeDefinition, error)
	List() []NodeDefinition
	HasNodeType(nodeType string) bool
}

// inMemoryRegistry is a concurrency-safe in-memory Registry storing both executable
// nodes and their static metadata definitions.
type inMemoryRegistry struct {
	mu    sync.RWMutex
	nodes map[string]Node
	defs  map[string]NodeDefinition
}

// NewRegistry creates a new, empty in-memory Node registry.
func NewRegistry() Registry {
	return &inMemoryRegistry{
		nodes: make(map[string]Node),
		defs:  make(map[string]NodeDefinition),
	}
}

// Register adds an executable node to the registry.
// Returns ErrInvalidNode if node is nil or Type() is empty.
// Returns ErrNodeAlreadyRegistered if a node of the same type is already registered.
func (r *inMemoryRegistry) Register(node Node) error {
	if node == nil {
		return fmt.Errorf("%w: nil node", ErrInvalidNode)
	}
	t := node.Type()
	if t == "" {
		return fmt.Errorf("%w: empty node type", ErrInvalidNode)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.nodes[t]; exists {
		return fmt.Errorf("%w: node type %q already registered", ErrNodeAlreadyRegistered, t)
	}
	r.nodes[t] = node
	return nil
}

// RegisterDefinition adds a static metadata definition to the registry.
// Returns validation errors or ErrDefinitionAlreadyRegistered if duplicate.
func (r *inMemoryRegistry) RegisterDefinition(def NodeDefinition) error {
	if err := def.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("%w: node definition %q already registered", ErrDefinitionAlreadyRegistered, def.Type)
	}
	r.defs[def.Type] = def
	return nil
}

// RegisterNode atomically registers an executable node and its matching definition,
// verifying type consistency upfront.
func (r *inMemoryRegistry) RegisterNode(node Node, def NodeDefinition) error {
	if node == nil {
		return fmt.Errorf("%w: nil node", ErrInvalidNode)
	}
	nodeType := node.Type()
	if nodeType == "" {
		return fmt.Errorf("%w: empty node type", ErrInvalidNode)
	}
	if def.Type != nodeType {
		return fmt.Errorf("%w: executable node type %q does not match definition type %q", ErrInconsistentType, nodeType, def.Type)
	}
	if err := def.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.nodes[nodeType]; exists {
		return fmt.Errorf("%w: node type %q already registered", ErrNodeAlreadyRegistered, nodeType)
	}
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("%w: node definition %q already registered", ErrDefinitionAlreadyRegistered, def.Type)
	}

	r.nodes[nodeType] = node
	r.defs[def.Type] = def
	return nil
}

// Get returns the executable Node for the given type name.
// Returns ErrNodeNotFound if no node is registered for that type.
func (r *inMemoryRegistry) Get(nodeType string) (Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n, exists := r.nodes[nodeType]
	if !exists {
		return nil, fmt.Errorf("%w: node type %q is not registered", ErrNodeNotFound, nodeType)
	}
	return n, nil
}

// GetDefinition returns the static metadata definition for the given node type.
// Returns ErrDefinitionNotFound if no definition is registered for that type.
func (r *inMemoryRegistry) GetDefinition(nodeType string) (NodeDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, exists := r.defs[nodeType]
	if !exists {
		return NodeDefinition{}, fmt.Errorf("%w: node definition %q is not registered", ErrDefinitionNotFound, nodeType)
	}
	return d, nil
}

// Resolve provides direct compatibility with execution.NodeResolver.
func (r *inMemoryRegistry) Resolve(nodeType string) (Node, bool) {
	n, err := r.Get(nodeType)
	return n, err == nil
}

// HasNodeType reports whether the given node type is registered.
func (r *inMemoryRegistry) HasNodeType(nodeType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, exists := r.nodes[nodeType]; exists {
		return true
	}
	_, exists := r.defs[nodeType]
	return exists
}

// List returns a deterministically sorted slice of all registered executable node types.
func (r *inMemoryRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.nodes))
	for k := range r.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Definitions returns a deterministically sorted slice of all registered node definitions.
func (r *inMemoryRegistry) Definitions() []NodeDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := make([]NodeDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Type < res[j].Type
	})
	return res
}

// Validate verifies registry consistency:
// - Every registered node has a non-empty Type() and corresponding valid definition.
// - Every registered definition has a corresponding executable node.
// - Types between executable nodes and definitions match exactly.
func (r *inMemoryRegistry) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for nodeType, n := range r.nodes {
		if n == nil {
			return fmt.Errorf("%w: registered node %q is nil", ErrInvalidNode, nodeType)
		}
		if n.Type() != nodeType {
			return fmt.Errorf("%w: node key %q does not match node.Type() %q", ErrInconsistentType, nodeType, n.Type())
		}
		def, exists := r.defs[nodeType]
		if !exists {
			return fmt.Errorf("%w: node %q has executable implementation but no definition", ErrMissingDefinition, nodeType)
		}
		if def.Type != nodeType {
			return fmt.Errorf("%w: node %q matches definition with mismatched type %q", ErrInconsistentType, nodeType, def.Type)
		}
		if err := def.Validate(); err != nil {
			return fmt.Errorf("%w: definition for %q is invalid: %v", ErrInvalidDefinition, nodeType, err)
		}
	}

	for defType, def := range r.defs {
		if err := def.Validate(); err != nil {
			return fmt.Errorf("%w: definition %q is invalid: %v", ErrInvalidDefinition, defType, err)
		}
		if _, exists := r.nodes[defType]; !exists {
			return fmt.Errorf("%w: definition %q has no corresponding executable node", ErrMissingNode, defType)
		}
	}

	return nil
}

// inMemoryDefinitionRegistry is a standalone in-memory DefinitionRegistry.
type inMemoryDefinitionRegistry struct {
	mu   sync.RWMutex
	defs map[string]NodeDefinition
}

// NewDefinitionRegistry creates a new, empty in-memory DefinitionRegistry.
func NewDefinitionRegistry() DefinitionRegistry {
	return &inMemoryDefinitionRegistry{
		defs: make(map[string]NodeDefinition),
	}
}

// Register validates and registers a NodeDefinition.
func (r *inMemoryDefinitionRegistry) Register(def NodeDefinition) error {
	if err := def.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("%w: node definition %q already registered", ErrDefinitionAlreadyRegistered, def.Type)
	}
	r.defs[def.Type] = def
	return nil
}

// Get looks up a NodeDefinition by its node type identifier.
func (r *inMemoryDefinitionRegistry) Get(nodeType string) (NodeDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, exists := r.defs[nodeType]
	if !exists {
		return NodeDefinition{}, fmt.Errorf("%w: node definition %q is not registered", ErrDefinitionNotFound, nodeType)
	}
	return d, nil
}

// HasNodeType reports whether the definition is present.
func (r *inMemoryDefinitionRegistry) HasNodeType(nodeType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.defs[nodeType]
	return exists
}

// List returns all registered definitions deterministically sorted by node type.
func (r *inMemoryDefinitionRegistry) List() []NodeDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := make([]NodeDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Type < res[j].Type
	})
	return res
}

// ValidateRegistries checks bidirectional consistency between an executable node
// registry and a definition registry.
func ValidateRegistries(nodes Registry, defs DefinitionRegistry) error {
	if nodes == nil {
		return fmt.Errorf("%w: nil node registry", ErrRegistryValidation)
	}
	if defs == nil {
		return fmt.Errorf("%w: nil definition registry", ErrRegistryValidation)
	}

	nodeTypes := nodes.List()
	for _, t := range nodeTypes {
		n, err := nodes.Get(t)
		if err != nil {
			return fmt.Errorf("%w: lookup node %q: %v", ErrRegistryValidation, t, err)
		}
		if n.Type() != t {
			return fmt.Errorf("%w: node key %q does not match Type() %q", ErrInconsistentType, t, n.Type())
		}
		def, err := defs.Get(t)
		if err != nil {
			return fmt.Errorf("%w: node %q has executable implementation but no definition: %v", ErrMissingDefinition, t, err)
		}
		if def.Type != t {
			return fmt.Errorf("%w: node %q matches definition with mismatched type %q", ErrInconsistentType, t, def.Type)
		}
		if err := def.Validate(); err != nil {
			return fmt.Errorf("%w: definition for node %q is invalid: %v", ErrInvalidDefinition, t, err)
		}
	}

	allDefs := defs.List()
	for _, def := range allDefs {
		if err := def.Validate(); err != nil {
			return fmt.Errorf("%w: definition %q is invalid: %v", ErrInvalidDefinition, def.Type, err)
		}
		if _, err := nodes.Get(def.Type); err != nil {
			return fmt.Errorf("%w: definition %q has no corresponding executable node", ErrMissingNode, def.Type)
		}
	}

	return nil
}
