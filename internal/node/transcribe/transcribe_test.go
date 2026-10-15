package transcribe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/transcribe"
	providerllm "workflow-optimizer/internal/provider/llm"
)

type stored struct {
	f    files.File
	data []byte
}

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

// resolver knows one Groq key.
type resolver struct{ id uuid.UUID }

func (r resolver) Resolve(_ context.Context, _, id uuid.UUID, provider string) (credential.ResolvedCredential, error) {
	if id != r.id {
		return credential.ResolvedCredential{}, credential.ErrNotFound
	}
	if provider != "groq" {
		return credential.ResolvedCredential{}, credential.ErrProviderMismatch
	}
	return credential.ResolvedCredential{ID: id, Provider: "groq", Type: credential.TypeAPIKey, Secret: credential.NewSecret("gsk-test")}, nil
}

type fakeTranscriber struct {
	got  providerllm.TranscribeRequest
	fail error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, req providerllm.TranscribeRequest) (providerllm.TranscribeResponse, error) {
	f.got = req
	if f.fail != nil {
		return providerllm.TranscribeResponse{}, f.fail
	}
	return providerllm.TranscribeResponse{Text: "Let's ship on Friday."}, nil
}

func (f *fakeTranscriber) TranscriptionModels() []providerllm.Model {
	return []providerllm.Model{{ID: "whisper-large-v3-turbo", Label: "Whisper"}}
}

func TestTranscribe(t *testing.T) {
	ws := uuid.New()
	mp3 := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "standup.mp3", ContentType: "audio/mpeg"}
	pdf := files.File{ID: uuid.New(), WorkspaceID: ws, Name: "notes.pdf", ContentType: "application/pdf"}
	r := reader{mp3.ID: {mp3, []byte("ID3audio")}, pdf.ID: {pdf, []byte("%PDF")}}
	key := uuid.New()
	tr := &fakeTranscriber{}
	n := transcribe.New(transcribe.Dependencies{Providers: []transcribe.Provider{{Name: "groq", Label: "Groq", Transcriber: tr}},
		Credentials: resolver{key}, Files: r})
	run := func(cfg map[string]any, ports map[string]node.Value) (map[string]any, error) {
		base := map[string]any{"provider": "groq", "model": "whisper-large-v3-turbo", "credential_id": key.String()}
		for k, v := range cfg {
			base[k] = v
		}
		out, err := n.Execute(context.Background(), node.NodeInput{Config: base, Ports: ports, Scope: node.Scope{WorkspaceID: ws}})
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		for k, v := range out.Ports {
			m[k] = v.Data
		}
		return m, nil
	}

	out, err := run(map[string]any{"audio_file": mp3.ID.String(), "language": " EN "}, nil)
	if err != nil || out["text"] != "Let's ship on Friday." || out["name"] != "standup.mp3" {
		t.Fatalf("%v %v", out, err)
	}
	if tr.got.Model != "whisper-large-v3-turbo" || string(tr.got.Audio) != "ID3audio" || tr.got.Filename != "standup.mp3" ||
		tr.got.Language != "en" || tr.got.Credential.Secret.Reveal() != "gsk-test" {
		t.Fatalf("request: %+v", tr.got)
	}
	// A connected file ID (or {file_id}) wins over the setting.
	if _, err := run(map[string]any{"audio_file": pdf.ID.String()}, map[string]node.Value{"audio": node.NewJSONValue(map[string]any{"file_id": mp3.ID.String()})}); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		cfg  map[string]any
		code node.ErrorCode
	}{
		"no recording":  {map[string]any{}, node.ErrCodeConfiguration},
		"not audio":     {map[string]any{"audio_file": pdf.ID.String()}, node.ErrCodeInvalidInput},
		"missing file":  {map[string]any{"audio_file": uuid.NewString()}, node.ErrCodeInvalidInput},
		"wrong service": {map[string]any{"audio_file": mp3.ID.String(), "provider": "gemini"}, node.ErrCodeConfiguration},
		"no credential": {map[string]any{"audio_file": mp3.ID.String(), "credential_id": ""}, "CREDENTIAL_INVALID"},
		"unknown key":   {map[string]any{"audio_file": mp3.ID.String(), "credential_id": uuid.NewString()}, "CREDENTIAL_NOT_FOUND"},
	} {
		_, err := run(c.cfg, nil)
		var ne *node.NodeError
		if !errors.As(err, &ne) || ne.Code != c.code || ne.Retryable {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// Provider failures keep their classification.
	tr.fail = &providerllm.Error{Provider: "groq", Code: providerllm.CodeRateLimited, Retryable: true, NotApplied: true}
	_, err = run(map[string]any{"audio_file": mp3.ID.String()}, nil)
	var ne *node.NodeError
	if !errors.As(err, &ne) || ne.Code != "RATE_LIMITED" || !ne.Retryable || !ne.NotApplied {
		t.Fatalf("rate limited: %v", err)
	}

	// The recording picker lists audio files only.
	opts, err := n.Options(context.Background(), node.Scope{WorkspaceID: ws}, "audio_files", nil)
	if err != nil || len(opts) != 1 || opts[0].Label != "standup.mp3" {
		t.Fatalf("options: %v %v", opts, err)
	}
	def := transcribe.Definition([]transcribe.Provider{{Name: "groq", Label: "Groq", Transcriber: tr}})
	if def.Category != node.CategoryMultiModal || len(def.Config[1].Options) != 1 || def.Config[1].Options[0].When["provider"] != "groq" {
		t.Fatalf("definition: %+v", def)
	}
}
