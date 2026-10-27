package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Contact struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Status struct {
	State string `json:"state"`
	QR    string `json:"qr,omitempty"`
}
type SendRequest struct {
	Recipient   string `json:"recipient"`
	Text        string `json:"text"`
	OperationID string `json:"operation_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}
type Receipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func (c *Client) Request(ctx context.Context, ws, user uuid.UUID, method, action string, body, out any) error {
	if c.Token == "" {
		return errors.New("connect WhatsApp in Settings first")
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+"/accounts/"+ws.String()+"/"+user.String()+"/"+action, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("WhatsApp connection is unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return errors.New("WhatsApp could not finish; check its connection and recipient")
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 30<<20)).Decode(out)
	}
	return nil
}
func (c *Client) Contacts(ctx context.Context, ws, user uuid.UUID) ([]Contact, error) {
	var out []Contact
	err := c.Request(ctx, ws, user, "GET", "contacts", nil, &out)
	return out, err
}
func (c *Client) Send(ctx context.Context, ws, user uuid.UUID, in SendRequest) (Receipt, error) {
	var out Receipt
	err := c.Request(ctx, ws, user, "POST", "send", in, &out)
	return out, err
}
