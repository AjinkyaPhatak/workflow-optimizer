package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/provider/llm"
)

// MaxEmbedInputs bounds one embeddings request.
const MaxEmbedInputs = 256

var _ llm.Embedder = (*Provider)(nil)

type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed implements llm.Embedder with the Embeddings API. Errors are
// classified like Generate's (the response's message is never kept).
func (p *Provider) Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	if err := ctx.Err(); err != nil {
		return llm.EmbedResponse{}, err
	}
	cred := req.Credential
	if cred.Provider != Name || cred.Type != credential.TypeAPIKey || cred.Secret.Empty() {
		return llm.EmbedResponse{}, fmt.Errorf("%w: openai requires an %s credential for provider %q", credential.ErrInvalid, credential.TypeAPIKey, Name)
	}
	if strings.TrimSpace(req.Model) == "" || len(req.Inputs) == 0 || len(req.Inputs) > MaxEmbedInputs {
		return llm.EmbedResponse{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("a model and 1 to %d inputs are required", MaxEmbedInputs)}
	}
	body, _ := json.Marshal(embedRequest{Model: req.Model, Input: req.Inputs, Dimensions: req.Dimensions, EncodingFormat: "float"})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return llm.EmbedResponse{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidRequest, Message: "request could not be built"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.Secret.Reveal())
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.EmbedResponse{}, transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return llm.EmbedResponse{}, transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return llm.EmbedResponse{}, statusError(resp, raw)
	}
	var parsed embedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Data) != len(req.Inputs) {
		return llm.EmbedResponse{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode, Retryable: true,
			Message: "response is not one embedding per input"}
	}
	out := llm.EmbedResponse{Vectors: make([][]float32, len(req.Inputs)), Tokens: parsed.Usage.TotalTokens}
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out.Vectors) || len(d.Embedding) == 0 || (req.Dimensions > 0 && len(d.Embedding) != req.Dimensions) {
			return llm.EmbedResponse{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode, Retryable: true,
				Message: "response has an unexpected embedding"}
		}
		out.Vectors[d.Index] = d.Embedding
	}
	for _, v := range out.Vectors {
		if v == nil {
			return llm.EmbedResponse{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode, Retryable: true,
				Message: "response is missing an embedding"}
		}
	}
	return out, nil
}
