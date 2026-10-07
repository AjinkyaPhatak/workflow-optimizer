package integration

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"workflow-optimizer/internal/node"
)

// Registry errors.
var (
	ErrDuplicate = errors.New("integration: already registered")
	ErrNotFound  = errors.New("integration: not found")
	ErrFrozen    = errors.New("integration: registry is frozen")
)

// Registry is the discovery registry of integrations: metadata only. The
// node registry remains the registry of executable implementations.
// Registration happens at startup; after Freeze it is read-only and safe for
// concurrent reads. Listings are sorted by ID.
type Registry interface {
	Register(in Integration) error
	Get(id string) (Integration, error)
	// Action looks an action up by its node type ("gmail.send").
	Action(nodeType string) (Integration, Action, error)
	List() []Integration
	Actions(integrationID string) ([]Action, error)
	Freeze()
}

type inMemoryRegistry struct {
	mu     sync.RWMutex
	items  map[string]Integration
	frozen bool
}

// NewRegistry returns an empty registry.
func NewRegistry() Registry {
	return &inMemoryRegistry{items: map[string]Integration{}}
}

// Register validates and adds an integration. A deep enough copy is kept
// that later changes to the caller's slices do not affect it.
func (r *inMemoryRegistry) Register(in Integration) error {
	if err := in.Validate(); err != nil {
		return err
	}
	in.Actions = append([]Action{}, in.Actions...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return fmt.Errorf("%w: cannot register %q", ErrFrozen, in.ID)
	}
	if _, ok := r.items[in.ID]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, in.ID)
	}
	r.items[in.ID] = in
	return nil
}

func (r *inMemoryRegistry) Get(id string) (Integration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.items[id]
	if !ok {
		return Integration{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return in, nil
}

func (r *inMemoryRegistry) Action(nodeType string) (Integration, Action, error) {
	id, action, ok := SplitNodeType(nodeType)
	if !ok {
		return Integration{}, Action{}, fmt.Errorf("%w: %q is not an integration node type", ErrNotFound, nodeType)
	}
	in, err := r.Get(id)
	if err != nil {
		return Integration{}, Action{}, err
	}
	a, ok := in.Action(action)
	if !ok {
		return Integration{}, Action{}, fmt.Errorf("%w: action %q", ErrNotFound, nodeType)
	}
	return in, a, nil
}

func (r *inMemoryRegistry) List() []Integration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Integration, 0, len(r.items))
	for _, in := range r.items {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *inMemoryRegistry) Actions(integrationID string) ([]Action, error) {
	in, err := r.Get(integrationID)
	if err != nil {
		return nil, err
	}
	return append([]Action{}, in.Actions...), nil
}

func (r *inMemoryRegistry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Module is an integration together with the executable node of each of its
// actions, keyed by action ID.
type Module struct {
	Integration Integration
	Nodes       map[string]node.Node
}

// Install registers a module: its metadata in the integration registry and
// every action as an ordinary node (implementation + definition) in the node
// registry. Every action needs exactly one node, whose Type() is the
// action's node type.
func Install(integrations Registry, nodes node.Registry, m Module) error {
	in := m.Integration
	if err := in.Validate(); err != nil {
		return err
	}
	for id := range m.Nodes {
		if _, ok := in.Action(id); !ok {
			return fmt.Errorf("%w: node given for unknown action %q of %q", ErrInvalid, id, in.ID)
		}
	}
	for _, a := range in.Actions {
		if m.Nodes[a.ID] == nil {
			return fmt.Errorf("%w: action %q has no executable node", ErrInvalid, NodeType(in.ID, a.ID))
		}
	}
	if err := integrations.Register(in); err != nil {
		return err
	}
	for _, a := range in.Actions {
		if err := nodes.RegisterNode(m.Nodes[a.ID], in.Definition(a)); err != nil {
			return fmt.Errorf("install %q: %w", NodeType(in.ID, a.ID), err)
		}
	}
	return nil
}
