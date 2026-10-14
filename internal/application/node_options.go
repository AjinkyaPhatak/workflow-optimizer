package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workspace"
)

// NodeLookup finds executable nodes and their definitions (the node
// registry).
type NodeLookup interface {
	Resolve(nodeType string) (node.Node, bool)
	GetDefinition(nodeType string) (node.NodeDefinition, error)
}

// NodeOptionsService lists a node setting's choices through the workspace's
// connected account (Phase C8): Notion databases, Discord channels, sheet
// columns... It runs no action and changes nothing.
type NodeOptionsService struct {
	access  *Access
	nodes   NodeLookup
	timeout time.Duration
}

// NewNodeOptionsService wires the service.
func NewNodeOptionsService(access *Access, nodes NodeLookup) *NodeOptionsService {
	return &NodeOptionsService{access: access, nodes: nodes, timeout: 15 * time.Second}
}

// NodeOptionsRequest asks for one list of choices. Config is the node's
// current settings (its credential_id and the source's dependencies).
type NodeOptionsRequest struct {
	WorkspaceID uuid.UUID
	NodeType    string
	Source      string
	Config      map[string]any
}

// Options returns the choices. Anyone who may edit workflows in the
// workspace may list them (they can already run nodes with its accounts).
func (s *NodeOptionsService) Options(ctx context.Context, user uuid.UUID, req NodeOptionsRequest) ([]node.ConfigOption, error) {
	if _, err := s.access.Require(ctx, user, req.WorkspaceID, workspace.ActionWrite, "workspace"); err != nil {
		return nil, err
	}
	def, err := s.nodes.GetDefinition(req.NodeType)
	if err != nil {
		return nil, notFound("node type")
	}
	declared := false
	for _, f := range def.Config {
		declared = declared || f.OptionsSource == req.Source || f.KeysSource == req.Source
	}
	n, _ := s.nodes.Resolve(req.NodeType)
	p, ok := n.(integration.OptionsProvider)
	if !declared || !ok {
		return nil, &InvalidError{Message: fmt.Sprintf("%s has no %q choices", req.NodeType, req.Source)}
	}
	// Only settings, never references: a {{variable}} has no value yet.
	cfg := map[string]any{}
	for k, v := range req.Config {
		if str, isStr := v.(string); isStr && strings.Contains(str, "{{") {
			continue
		}
		cfg[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	opts, err := p.Options(ctx, node.Scope{WorkspaceID: req.WorkspaceID}, req.Source, cfg)
	if err != nil {
		return nil, optionsError(err)
	}
	return opts, nil
}

// optionsError turns a node failure into a message for the editor. The
// messages are the platform's own (credential and integration errors never
// carry provider bodies or secrets).
func optionsError(err error) error {
	var ie *integration.Error
	if errors.As(err, &ie) && ie.Message != "" {
		return &InvalidError{Message: ie.Message}
	}
	var ne *node.NodeError
	if errors.As(err, &ne) {
		switch ne.Code {
		case integration.ErrCodeCredentialNotFound:
			return &InvalidError{Message: "the selected account was not found in this workspace"}
		case integration.ErrCodeCredentialRevoked:
			return &InvalidError{Message: "the selected account must be connected again"}
		}
		return &InvalidError{Message: ne.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &InvalidError{Message: "the service took too long to answer"}
	}
	return err
}
