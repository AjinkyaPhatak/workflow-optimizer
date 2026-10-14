// Package extract is the Extract Text node (Phase D1): the text of a
// workspace file (PDF, Word, HTML, plain text, Markdown, CSV, JSON).
package extract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
)

// NodeType is the node's type.
const NodeType = "extract_text"

// Definition is the node's metadata.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type: NodeType, Name: "Extract Text", Category: node.CategoryKnowledgeBase,
		Description: "Reads the text of an uploaded file: PDF, Word (.docx), HTML, plain text, Markdown, CSV or JSON.",
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("file", node.ValueTypeJSON, false, "Any upstream data; a file ID (or an object with file_id) is used as the file"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("text", node.ValueTypeString, true, "The file's text"),
			node.NewPortDefinition("name", node.ValueTypeString, true, "File name"),
			node.NewPortDefinition("content_type", node.ValueTypeString, true, "File type, e.g. application/pdf"),
			node.NewPortDefinition("pages", node.ValueTypeNumber, true, "Page count (PDF; 0 otherwise)"),
			node.NewPortDefinition("truncated", node.ValueTypeBoolean, true, "Whether the text was cut at 2 MB"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("file_id", node.ValueTypeString, false, "", "A file uploaded on the Files page. Used when no file is connected.").
				WithLabel("File").WithOptionsSource("files"),
		},
	}
}

// Node is the Extract Text node.
type Node struct {
	files files.Reader
}

// New returns the node reading through r (nil: every run fails with a
// configuration error).
func New(r files.Reader) *Node { return &Node{files: r} }

// Type implements node.Node.
func (n *Node) Type() string { return NodeType }

// FileID is the file a node should read: a connected file ID (text, or an
// object with file_id), else the file_id setting.
func FileID(in node.NodeInput, port, setting string) (uuid.UUID, bool, error) {
	raw := ""
	if v, ok := in.GetPort(port); ok {
		switch d := v.Data.(type) {
		case string:
			raw = d
		case map[string]any:
			raw, _ = d["file_id"].(string)
		}
	}
	if strings.TrimSpace(raw) == "" {
		raw, _ = in.GetStringConfig(setting)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil, false, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, true, node.NewNodeError(node.ErrCodeInvalidInput, "the file reference is not a file ID", false)
	}
	return id, true, nil
}

// ReadError maps a file read failure to a node error.
func ReadError(err error) error {
	if errors.Is(err, files.ErrNotFound) {
		return node.NewNodeError(node.ErrCodeInvalidInput, "the file was not found in this workspace", false)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return node.WrapNodeError(node.ErrCodeUnavailable, "the file could not be read", true, err)
}

// Execute implements node.Node.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if n.files == nil {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "file storage is not available", false)
	}
	id, ok, err := FileID(in, "file", "file_id")
	if err != nil {
		return node.NodeOutput{}, err
	}
	if !ok {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "choose a File, or connect a file ID", false)
	}
	f, data, err := n.files.Content(ctx, in.Scope.WorkspaceID, id)
	if err != nil {
		return node.NodeOutput{}, ReadError(err)
	}
	x, err := files.ExtractText(f, data)
	if err != nil {
		msg := strings.TrimPrefix(strings.TrimPrefix(err.Error(), files.ErrUnsupported.Error()), files.ErrInvalid.Error()+": ")
		if errors.Is(err, files.ErrUnsupported) {
			msg = fmt.Sprintf("text cannot be extracted from %s (%s)", f.Name, f.ContentType)
		}
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeInvalidInput, msg, false)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("text", node.NewStringValue(x.Text))
	out.SetPort("name", node.NewStringValue(f.Name))
	out.SetPort("content_type", node.NewStringValue(f.ContentType))
	out.SetPort("pages", node.NewNumberValue(float64(x.Pages)))
	out.SetPort("truncated", node.NewBooleanValue(x.Truncated))
	return out, nil
}

// FileOptions lists a workspace's files as choices; only lists files whose
// type keep accepts (nil: all).
func FileOptions(ctx context.Context, r files.Reader, workspaceID uuid.UUID, keep func(files.File) bool) ([]node.ConfigOption, error) {
	if r == nil {
		return nil, node.NewNodeError(node.ErrCodeConfiguration, "file storage is not available", false)
	}
	all, err := r.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []node.ConfigOption
	for _, f := range all {
		if keep == nil || keep(f) {
			out = append(out, node.ConfigOption{Value: f.ID.String(), Label: f.Name})
		}
	}
	return out, nil
}

// OptionSources implements the options provider contract (Phase C8).
func (n *Node) OptionSources() []string { return []string{"files"} }

// Options lists the workspace's files.
func (n *Node) Options(ctx context.Context, scope node.Scope, source string, _ map[string]any) ([]node.ConfigOption, error) {
	if source != "files" {
		return nil, fmt.Errorf("extract_text has no %q options", source)
	}
	return FileOptions(ctx, n.files, scope.WorkspaceID, nil)
}
