package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/llm"
	providerllm "workflow-optimizer/internal/provider/llm"
)

type imageStore map[uuid.UUID]struct {
	f    files.File
	data []byte
}

func (s imageStore) Content(_ context.Context, ws, id uuid.UUID) (files.File, []byte, error) {
	e, ok := s[id]
	if !ok || e.f.WorkspaceID != ws {
		return files.File{}, nil, files.ErrNotFound
	}
	return e.f, e.data, nil
}

func (s imageStore) List(_ context.Context, ws uuid.UUID) ([]files.File, error) {
	var out []files.File
	for _, e := range s {
		if e.f.WorkspaceID == ws {
			out = append(out, e.f)
		}
	}
	return out, nil
}

// Phase D3: images for models that accept them.
func TestImages(t *testing.T) {
	ws := uuid.New()
	png := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "receipt.png", ContentType: "image/png"}
	jpg := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "photo.jpg", ContentType: "image/jpeg"}
	pdf := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "doc.pdf", ContentType: "application/pdf"}
	foreign := files.File{ID: uuid.New(), WorkspaceID: uuid.New(), Name: "x.png", ContentType: "image/png"}
	store := imageStore{}
	for _, f := range []files.File{png, jpg, pdf, foreign} {
		store[f.ID] = struct {
			f    files.File
			data []byte
		}{f, []byte("bytes-of-" + f.Name)}
	}
	p := &fakeProvider{resp: providerllm.Response{Content: "Total: $42"}}
	reg := providerllm.NewRegistry()
	_ = reg.Register("fake", p)
	reg.Freeze()
	n := llm.New(llm.Dependencies{Providers: reg, Credentials: &fakeResolver{}, Files: store})

	in := input(ws, uuid.New(), map[string]any{"image_file": png.ID.String(), "image_urls": "https://example.test/a.png\n\n  https://example.test/b.webp  "})
	in.Ports["images"] = node.NewArrayValue([]any{map[string]any{"file_id": jpg.ID.String()}, map[string]any{"url": "https://example.test/c.gif"}})
	if _, err := n.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	imgs := p.got.Messages[0].Images
	if len(imgs) != 5 || imgs[0].MIMEType != "image/jpeg" || string(imgs[0].Data) != "bytes-of-photo.jpg" || imgs[1].URL != "https://example.test/c.gif" ||
		string(imgs[2].Data) != "bytes-of-receipt.png" || imgs[3].URL != "https://example.test/a.png" || imgs[4].URL != "https://example.test/b.webp" {
		t.Fatalf("images: %+v", imgs)
	}

	for name, cfg := range map[string]map[string]any{
		"not an image":    {"image_file": pdf.ID.String()},
		"other workspace": {"image_file": foreign.ID.String()},
		"plain http":      {"image_urls": "http://example.test/a.png"},
		"not a reference": {"image_urls": "receipt.png"},
		"too many":        {"image_urls": strings.Repeat("https://example.test/x.png\n", llm.MaxImages+1)},
	} {
		_, err := n.Execute(context.Background(), input(ws, uuid.New(), cfg))
		var ne *node.NodeError
		if !errors.As(err, &ne) || ne.Code != node.ErrCodeInvalidInput || ne.Retryable {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// No file storage: a file reference fails clearly; URLs still work.
	bare := llm.New(llm.Dependencies{Providers: reg, Credentials: &fakeResolver{}})
	if _, err := bare.Execute(context.Background(), input(ws, uuid.New(), map[string]any{"image_file": png.ID.String()})); err == nil {
		t.Fatal("a file without storage must fail")
	}
	if _, err := bare.Execute(context.Background(), input(ws, uuid.New(), map[string]any{"image_urls": "https://example.test/a.png"})); err != nil {
		t.Fatal(err)
	}
	opts, err := n.Options(context.Background(), node.Scope{WorkspaceID: ws}, "image_files", nil)
	if err != nil || len(opts) != 2 {
		t.Fatalf("image files only: %v %v", opts, err)
	}
	def := llm.Definition()
	if f, _ := def.GetConfigField("image_file"); f.OptionsSource != "image_files" {
		t.Fatalf("image field: %+v", f)
	}
}
