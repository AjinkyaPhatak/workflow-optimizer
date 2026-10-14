package llm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
	providerllm "workflow-optimizer/internal/provider/llm"
)

// Image limits (Phase D3).
const (
	MaxImages     = 10
	MaxImageBytes = 20 << 20
	// maxTotalImageBytes bounds the images of one request.
	maxTotalImageBytes = 30 << 20
)

func imageError(msg string) error { return node.NewNodeError(node.ErrCodeInvalidInput, msg, false) }

// imageRefs collects the image references of a run: the images input (a
// file ID or https URL, an object with file_id or url, or a list of these),
// then the Image setting (a file ID), then the Image URLs setting (one per
// line).
func imageRefs(in node.NodeInput) []string {
	var refs []string
	var add func(v any)
	add = func(v any) {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				refs = append(refs, s)
			}
		case map[string]any:
			if id, ok := t["file_id"].(string); ok {
				add(id)
			} else if u, ok := t["url"].(string); ok {
				add(u)
			}
		case []any:
			for _, x := range t {
				add(x)
			}
		}
	}
	if v, ok := in.GetPort("images"); ok {
		add(v.Data)
	}
	if s, ok := in.GetStringConfig("image_file"); ok {
		add(s)
	}
	if s, ok := in.GetStringConfig("image_urls"); ok {
		for _, line := range strings.Split(s, "\n") {
			add(line)
		}
	}
	return refs
}

// images loads a run's images: workspace files are read (and must be PNG,
// JPEG, GIF or WebP images), https URLs are passed on for the provider to
// fetch.
func (n *Node) images(ctx context.Context, in node.NodeInput) ([]providerllm.Image, error) {
	refs := imageRefs(in)
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > MaxImages {
		return nil, imageError(fmt.Sprintf("at most %d images per request", MaxImages))
	}
	var (
		out   []providerllm.Image
		total int
	)
	for _, ref := range refs {
		if id, err := uuid.Parse(ref); err == nil {
			if n.deps.Files == nil {
				return nil, configError("file storage is not available")
			}
			f, data, err := n.deps.Files.Content(ctx, in.Scope.WorkspaceID, id)
			if errors.Is(err, files.ErrNotFound) {
				return nil, imageError("the image file was not found in this workspace")
			}
			if err != nil {
				return nil, node.WrapNodeError(node.ErrCodeUnavailable, "the image file could not be read", true, err)
			}
			if !files.IsImage(f.ContentType) {
				return nil, imageError(fmt.Sprintf("%s is not an image (PNG, JPEG, GIF or WebP)", f.Name))
			}
			if total += len(data); len(data) > MaxImageBytes || total > maxTotalImageBytes {
				return nil, imageError("the images are too large (20 MB each, 30 MB together)")
			}
			out = append(out, providerllm.Image{MIMEType: f.ContentType, Data: data})
			continue
		}
		u, err := url.Parse(ref)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, imageError("an image must be an uploaded image file or an https:// URL")
		}
		out = append(out, providerllm.Image{URL: u.String()})
	}
	return out, nil
}

// OptionSources implements the options provider contract (Phase C8).
func (n *Node) OptionSources() []string { return []string{"image_files"} }

// Options lists the workspace's image files.
func (n *Node) Options(ctx context.Context, scope node.Scope, source string, _ map[string]any) ([]node.ConfigOption, error) {
	if source != "image_files" || n.deps.Files == nil {
		return nil, configError("no such choices")
	}
	all, err := n.deps.Files.List(ctx, scope.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []node.ConfigOption
	for _, f := range all {
		if files.IsImage(f.ContentType) {
			out = append(out, node.ConfigOption{Value: f.ID.String(), Label: f.Name})
		}
	}
	return out, nil
}
