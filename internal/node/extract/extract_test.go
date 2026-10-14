package extract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/extract"
)

type stored struct {
	f    files.File
	data []byte
}

// reader is an in-memory files.Reader keyed by workspace.
type reader map[uuid.UUID]stored

func (r reader) Content(_ context.Context, ws, id uuid.UUID) (files.File, []byte, error) {
	s, ok := r[id]
	if !ok || s.f.WorkspaceID != ws {
		return files.File{}, nil, files.ErrNotFound
	}
	return s.f, s.data, nil
}

func (r reader) List(_ context.Context, ws uuid.UUID) ([]files.File, error) {
	var out []files.File
	for _, s := range r {
		if s.f.WorkspaceID == ws {
			out = append(out, s.f)
		}
	}
	return out, nil
}

func TestExtractText(t *testing.T) {
	ws, other := uuid.New(), uuid.New()
	txt := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "notes.md", ContentType: "text/markdown"}
	png := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "photo.png", ContentType: "image/png"}
	foreign := files.File{ID: uuid.New(), WorkspaceID: other, Name: "secret.txt", ContentType: "text/plain"}
	r := reader{txt.ID: {txt, []byte("# Plan\nShip it")}, png.ID: {png, []byte{1, 2}}, foreign.ID: {foreign, []byte("secret")}}
	n := extract.New(r)
	run := func(cfg map[string]any, ports map[string]node.Value) (map[string]any, error) {
		out, err := n.Execute(context.Background(), node.NodeInput{Config: cfg, Ports: ports, Scope: node.Scope{WorkspaceID: ws}})
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		for k, v := range out.Ports {
			m[k] = v.Data
		}
		return m, nil
	}
	out, err := run(map[string]any{"file_id": txt.ID.String()}, nil)
	if err != nil || out["text"] != "# Plan\nShip it" || out["name"] != "notes.md" || out["content_type"] != "text/markdown" || out["truncated"] != false {
		t.Fatalf("by setting: %v %v", out, err)
	}
	// A connected file ID (text or {file_id}) wins over the setting.
	if out, err := run(map[string]any{"file_id": png.ID.String()}, map[string]node.Value{"file": node.NewObjectValue(map[string]any{"file_id": txt.ID.String()})}); err != nil || out["name"] != "notes.md" {
		t.Fatalf("by port: %v %v", out, err)
	}
	code := func(cfg map[string]any) node.ErrorCode {
		t.Helper()
		_, err := run(cfg, nil)
		var ne *node.NodeError
		if !errors.As(err, &ne) || ne.Retryable {
			t.Fatalf("%v: %v", cfg, err)
		}
		return ne.Code
	}
	if c := code(map[string]any{"file_id": foreign.ID.String()}); c != node.ErrCodeInvalidInput {
		t.Fatalf("another workspace's file: %s", c)
	}
	if c := code(map[string]any{"file_id": png.ID.String()}); c != node.ErrCodeInvalidInput {
		t.Fatalf("image: %s", c)
	}
	if c := code(map[string]any{}); c != node.ErrCodeConfiguration {
		t.Fatalf("no file: %s", c)
	}
	if c := code(map[string]any{"file_id": "nope"}); c != node.ErrCodeInvalidInput {
		t.Fatalf("bad id: %s", c)
	}
	opts, err := n.Options(context.Background(), node.Scope{WorkspaceID: ws}, "files", nil)
	if err != nil || len(opts) != 2 {
		t.Fatalf("file options (own workspace only): %v %v", opts, err)
	}
	if _, err := extract.New(nil).Execute(context.Background(), node.NodeInput{Config: map[string]any{"file_id": txt.ID.String()}}); err == nil {
		t.Fatal("no storage must fail")
	}
	if err := extract.Definition().Validate(); err != nil {
		t.Fatal(err)
	}
}
