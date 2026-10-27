package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"workflow-optimizer/internal/provider/llm"
)

const Model = "intfloat/multilingual-e5-small"
const Revision = "614241f622f53c4eeff9890bdc4f31cfecc418b3"

type Embedder struct {
	URL    string
	Token  string
	Client *http.Client
}

func (e *Embedder) Embed(ctx context.Context, in llm.EmbedRequest) (llm.EmbedResponse, error) {
	payload, _ := json.Marshal(map[string]any{"texts": in.Inputs, "model": in.Model, "revision": Revision})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(e.URL, "/")+"/embed", bytes.NewReader(payload))
	if err != nil {
		return llm.EmbedResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.Token)
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	res, err := client.Do(req)
	if err != nil {
		return llm.EmbedResponse{}, errors.New("local document search is warming up or unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return llm.EmbedResponse{}, errors.New("local embedding worker could not process the document")
	}
	var out struct {
		Vectors [][]float32 `json:"vectors"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); err != nil {
		return llm.EmbedResponse{}, err
	}
	if len(out.Vectors) != len(in.Inputs) {
		return llm.EmbedResponse{}, errors.New("embedding worker returned an incomplete batch")
	}
	for _, v := range out.Vectors {
		if len(v) != 384 {
			return llm.EmbedResponse{}, errors.New("embedding model dimensions changed")
		}
	}
	return llm.EmbedResponse{Vectors: out.Vectors}, nil
}
