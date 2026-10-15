package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"workflow-optimizer/internal/provider/llm"
)

// MaxAudioBytes bounds one transcription upload (the Whisper APIs accept
// 25 MB).
const MaxAudioBytes = 25 << 20

// MaxChatAudioBytes bounds a recording sent inline to a chat model: Gemini
// takes requests up to 20 MB, and base64 makes the audio a third larger.
const MaxChatAudioBytes = 14 << 20

// transcribePrompt asks a chat model for a plain transcript.
const transcribePrompt = "Write down exactly what is said in this recording, in the language it is spoken. " +
	"Output only the transcript text: no title, no notes, no timestamps. If nothing is said, output nothing."

var _ llm.Transcriber = (*Provider)(nil)

// TranscriptionModels implements llm.Transcriber.
func (p *Provider) TranscriptionModels() []llm.Model {
	return append([]llm.Model(nil), p.spec.TranscriptionModels...)
}

// Transcribe implements llm.Transcriber with the audio transcriptions
// endpoint (multipart upload). Errors are classified like Generate's.
func (p *Provider) Transcribe(ctx context.Context, req llm.TranscribeRequest) (llm.TranscribeResponse, error) {
	if err := ctx.Err(); err != nil {
		return llm.TranscribeResponse{}, err
	}
	if err := p.checkCredential(req.Credential); err != nil {
		return llm.TranscribeResponse{}, err
	}
	if len(p.spec.TranscriptionModels) == 0 {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeModelNotSupported, Message: "this provider cannot transcribe audio"}
	}
	if p.spec.TranscribeWithChat {
		return p.transcribeWithChat(ctx, req)
	}
	if strings.TrimSpace(req.Model) == "" || len(req.Audio) == 0 || len(req.Audio) > MaxAudioBytes {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "a model and an audio file of at most 25 MB are required"}
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("model", req.Model)
	_ = w.WriteField("response_format", "json")
	if req.Language != "" {
		_ = w.WriteField("language", req.Language)
	}
	name := req.Filename
	if name == "" {
		name = "audio"
	}
	part, err := w.CreateFormFile("file", name)
	if err == nil {
		_, err = part.Write(req.Audio)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "request could not be encoded"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "request could not be built"}
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Credential.Secret.Reveal())
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.TranscribeResponse{}, p.transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return llm.TranscribeResponse{}, p.transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return llm.TranscribeResponse{}, p.statusError(resp, raw)
	}
	var parsed struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Text == nil {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode, Retryable: true,
			Message: "response is not a transcription"}
	}
	return llm.TranscribeResponse{Text: strings.TrimSpace(*parsed.Text)}, nil
}

// audioFormat is the input_audio format of a recording, from its file name
// (mp3 when unknown).
func audioFormat(filename string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")
	switch ext {
	case "mpga", "mpeg":
		return "mp3"
	case "oga", "opus":
		return "ogg"
	case "":
		return "mp3"
	}
	return ext
}

// transcribeWithChat sends the recording to a chat model that listens to
// audio, asking it to write down what is said.
func (p *Provider) transcribeWithChat(ctx context.Context, req llm.TranscribeRequest) (llm.TranscribeResponse, error) {
	if strings.TrimSpace(req.Model) == "" || len(req.Audio) == 0 || len(req.Audio) > MaxChatAudioBytes {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "a model and an audio file of at most 14 MB are required"}
	}
	prompt := transcribePrompt
	if req.Language != "" {
		prompt += " The recording is in the language with code " + req.Language + "."
	}
	body, err := json.Marshal(chatRequest{Model: req.Model, Messages: []chatMessage{{Role: string(llm.RoleUser), Content: []contentPart{
		{Type: "text", Text: prompt},
		{Type: "input_audio", InputAudio: &inputAudio{Data: base64.StdEncoding.EncodeToString(req.Audio), Format: audioFormat(req.Filename)}},
	}}}})
	if err != nil {
		return llm.TranscribeResponse{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "request could not be encoded"}
	}
	resp, err := p.complete(ctx, req.Credential, body)
	if err != nil {
		return llm.TranscribeResponse{}, err
	}
	return llm.TranscribeResponse{Text: strings.TrimSpace(resp.Content)}, nil
}
