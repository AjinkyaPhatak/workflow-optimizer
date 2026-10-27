package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"workflow-optimizer/internal/workspace"
)

type VoiceService struct {
	Access  *Access
	Key     string
	VoiceID string
	BaseURL string
	Client  *http.Client
}

func (s *VoiceService) endpoint() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return "https://api.elevenlabs.io"
}
func (s *VoiceService) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 45 * time.Second}
}
func (s *VoiceService) Token(ctx context.Context, user, ws uuid.UUID) (map[string]any, error) {
	if _, err := s.Access.Require(ctx, user, ws, workspace.ActionExecute, "workspace"); err != nil {
		return nil, err
	}
	if s.Key == "" {
		return nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint()+"/v1/single-use-token/realtime_scribe", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", s.Key)
	res, err := s.client().Do(req)
	if err != nil {
		return nil, &InvalidError{Message: "The voice service could not be reached."}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, &InvalidError{Message: "Voice could not start. Check the ElevenLabs connection."}
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16000)).Decode(&out); err != nil {
		return nil, err
	}
	return map[string]any{"token": out.Token, "model_id": "scribe_v2_realtime"}, nil
}
func (s *VoiceService) Speak(ctx context.Context, user, ws uuid.UUID, text string) (io.ReadCloser, error) {
	if _, err := s.Access.Require(ctx, user, ws, workspace.ActionExecute, "workspace"); err != nil {
		return nil, err
	}
	if s.Key == "" || s.VoiceID == "" {
		return nil, ErrUnavailable
	}
	if len(text) == 0 || len(text) > 3000 {
		return nil, &InvalidError{Message: "Spoken replies must contain between 1 and 3000 characters."}
	}
	body, _ := json.Marshal(map[string]any{"text": text, "model_id": "eleven_flash_v2_5"})
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint()+"/v1/text-to-speech/"+url.PathEscape(s.VoiceID)+"/stream", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", s.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	res, err := s.client().Do(req)
	if err != nil {
		return nil, errors.New("spoken reply is unavailable")
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, &InvalidError{Message: "The spoken reply could not be generated."}
	}
	return res.Body, nil
}
