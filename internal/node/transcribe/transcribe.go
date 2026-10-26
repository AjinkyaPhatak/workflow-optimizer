// Package transcribe is the Transcribe Audio node (Phase G): the text of an
// uploaded recording, from a provider that understands speech (Gemini,
// free; or Groq or OpenAI Whisper).
package transcribe

import (
	"context"
	"fmt"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/extract"
	"workflow-optimizer/internal/node/llm"
	providerllm "workflow-optimizer/internal/provider/llm"
)

// NodeType is the node's type.
const NodeType = "transcribe_audio"

// Provider is a speech-to-text provider with its registry name and label.
type Provider struct {
	Name, Label string
	Transcriber providerllm.Transcriber
}

// DefaultProvider and DefaultModel are the free Gemini tier.
const (
	DefaultProvider = "gemini"
	DefaultModel    = "gemini-3.5-flash-lite"
)

// Definition is the node's metadata; the provider and model choices are
// those of the given providers.
func Definition(providers []Provider) node.NodeDefinition {
	providerField := node.NewConfigField("provider", node.ValueTypeString, true, DefaultProvider, "Which service turns the speech into text.").WithLabel("Service")
	modelField := node.NewConfigField("model", node.ValueTypeString, true, DefaultModel, "The model that listens to the recording.").WithLabel("Model")
	if len(providers) > 0 {
		var provOpts, modelOpts []node.ConfigOption
		for _, p := range providers {
			provOpts = append(provOpts, node.Option(p.Name, p.Label))
			for _, m := range p.Transcriber.TranscriptionModels() {
				modelOpts = append(modelOpts, node.ConfigOption{Value: m.ID, Label: m.Label, When: map[string]any{"provider": p.Name}})
			}
		}
		providerField = providerField.WithOptions(provOpts...)
		modelField = modelField.WithOptions(modelOpts...).WithCustom()
	}
	return node.NodeDefinition{
		Type: NodeType, Name: "Transcribe Audio", Category: node.CategoryMultiModal,
		Description: "Turns a recording (MP3, M4A, WAV, OGG, WebM, FLAC or MP4) into text, e.g. a meeting to summarize. Gemini takes recordings up to 14 MB.",
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("audio", node.ValueTypeJSON, false, "Any upstream data; a file ID (or an object with file_id) is used as the recording"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("text", node.ValueTypeString, true, "What was said"),
			node.NewPortDefinition("name", node.ValueTypeString, true, "File name of the recording"),
		},
		Config: []node.ConfigField{
			providerField,
			modelField,
			node.NewConfigField("credential_id", node.ValueTypeString, false, "", "The API key of the service.").WithLabel("Credential"),
			node.NewConfigField("audio_file", node.ValueTypeString, false, "", "A recording uploaded on the Files page. Used when no recording is connected.").
				WithLabel("Recording").WithOptionsSource("audio_files"),
			node.NewConfigField("language", node.ValueTypeString, false, "", "The spoken language as a two-letter code (en, hi, es...). Empty: detected automatically.").WithLabel("Language"),
		},
	}
}

// Dependencies are the node's collaborators.
type Dependencies struct {
	Providers   []Provider
	Credentials credential.Resolver
	Files       files.Reader
}

// Node is the Transcribe Audio node.
type Node struct{ deps Dependencies }

// New returns the node.
func New(deps Dependencies) *Node { return &Node{deps: deps} }

// Type implements node.Node.
func (n *Node) Type() string { return NodeType }

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

// Execute implements node.Node.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	if n.deps.Files == nil || n.deps.Credentials == nil {
		return node.NodeOutput{}, configError("transcription is not available")
	}
	name, _ := in.GetStringConfig("provider")
	name = strings.TrimSpace(name)
	var provider *Provider
	for i := range n.deps.Providers {
		if n.deps.Providers[i].Name == name {
			provider = &n.deps.Providers[i]
		}
	}
	if provider == nil {
		return node.NodeOutput{}, configError(fmt.Sprintf("choose a Service that can transcribe (%q cannot)", name))
	}
	model, _ := in.GetStringConfig("model")
	if strings.TrimSpace(model) == "" {
		return node.NodeOutput{}, configError("choose a Model")
	}
	id, ok, err := extract.FileID(in, "audio", "audio_file")
	if err != nil {
		return node.NodeOutput{}, err
	}
	if !ok {
		return node.NodeOutput{}, configError("choose a Recording, or connect a file ID")
	}
	f, data, err := n.deps.Files.Content(ctx, in.Scope.WorkspaceID, id)
	if err != nil {
		return node.NodeOutput{}, extract.ReadError(err)
	}
	if !files.IsAudio(f.ContentType) {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeInvalidInput, fmt.Sprintf("%s is not a recording (MP3, M4A, WAV, OGG, WebM, FLAC or MP4)", f.Name), false)
	}
	cred, err := llm.ResolveCredential(ctx, n.deps.Credentials, in, provider.Name)
	if err != nil {
		return node.NodeOutput{}, err
	}
	language, _ := in.GetStringConfig("language")
	resp, err := provider.Transcriber.Transcribe(ctx, providerllm.TranscribeRequest{
		Model: strings.TrimSpace(model), Audio: data, Filename: f.Name, Language: strings.ToLower(strings.TrimSpace(language)), Credential: cred,
	})
	if err != nil {
		return node.NodeOutput{}, llm.ClassifyProviderError(err)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("text", node.NewStringValue(resp.Text))
	out.SetPort("name", node.NewStringValue(f.Name))
	return out, nil
}

// OptionSources implements the options provider contract (Phase C8).
func (n *Node) OptionSources() []string { return []string{"audio_files"} }

// Options lists the workspace's recordings.
func (n *Node) Options(ctx context.Context, scope node.Scope, source string, _ map[string]any) ([]node.ConfigOption, error) {
	if source != "audio_files" {
		return nil, configError("no such choices")
	}
	return extract.FileOptions(ctx, n.deps.Files, scope.WorkspaceID, func(f files.File) bool { return files.IsAudio(f.ContentType) })
}
