package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// DefaultBaseURL is the Discord REST API root.
const DefaultBaseURL = "https://discord.com/api/v10"

// DefaultTimeout bounds one API request.
const DefaultTimeout = 30 * time.Second

const maxResponseBytes = 8 << 20

// userAgent is the form Discord requires for bots.
const userAgent = "DiscordBot (https://github.com/workflow-optimizer, 1.0)"

// Options configure the Discord client. BotToken (DISCORD_BOT_TOKEN)
// authenticates every call; tests point BaseURL at a mock API.
type Options struct {
	BotToken   credential.Secret
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the Discord REST client for one action call, confined to one
// server (guild): the connected account's. It holds the bot token in memory
// only and sends it only as the Authorization header to the Discord API.
type Client struct {
	base    string
	http    *http.Client
	timeout time.Duration
	token   credential.Secret
	guild   string
}

// NewClient returns a client acting in guild.
func NewClient(o Options, guild string) *Client {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{base: base, http: hc, timeout: timeout, token: o.BotToken, guild: guild}
}

// Guild is the server the client is confined to.
func (c *Client) Guild() string { return c.guild }

// apiError is Discord's error body. Only the numeric code and retry_after
// are read; the message is never copied into errors.
type apiError struct {
	Code       int     `json:"code"`
	RetryAfter float64 `json:"retry_after"`
}

func classify(resp *http.Response, body []byte, what string) *integration.Error {
	var a apiError
	_ = json.Unmarshal(body, &a)
	retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if retryAfter == 0 && a.RetryAfter > 0 {
		retryAfter = time.Duration(a.RetryAfter * float64(time.Second))
	}
	e := integration.FromHTTPStatus(resp.StatusCode, retryAfter, what+" failed")
	switch e.Kind {
	case integration.KindAuthentication:
		e.Message = what + " failed: Discord rejected the bot token (check DISCORD_BOT_TOKEN)"
	case integration.KindPermissionDenied:
		e.Message = what + " failed: the bot is not allowed to do this in that channel (check its permissions there)"
	case integration.KindNotFound:
		e.Message = what + " failed: not found (or the bot cannot see it)"
	}
	return e
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any, what string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return integration.NewError(integration.KindInvalidRequest, what+": the request could not be encoded")
		}
		body = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return integration.NewError(integration.KindInvalidRequest, what+": invalid request")
	}
	req.Header.Set("Authorization", "Bot "+c.token.Reveal())
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err // classified by integration.ToNodeError, never printed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classify(resp, data, what)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return integration.NewError(integration.KindMalformedResponse, what+": Discord returned a malformed response")
	}
	return nil
}

// Channel is a server channel.
type Channel struct {
	ID      string `json:"id"`
	GuildID string `json:"guild_id"`
	Name    string `json:"name"`
	Type    int    `json:"type"`
}

// GetChannel loads a channel by ID.
func (c *Client) GetChannel(ctx context.Context, id string) (Channel, error) {
	var ch Channel
	err := c.do(ctx, http.MethodGet, "/channels/"+url.PathEscape(id), nil, nil, &ch, "open channel")
	return ch, err
}

// Channels lists the server's channels.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	err := c.do(ctx, http.MethodGet, "/guilds/"+url.PathEscape(c.guild)+"/channels", nil, nil, &out, "list channels")
	return out, err
}

// Author is a message's author.
type Author struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

// Message is a channel message.
type Message struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp"`
	Author      Author `json:"author"`
	Attachments []struct {
		Filename string `json:"filename"`
	} `json:"attachments"`
}

// Send posts one message.
func (c *Client) Send(ctx context.Context, channelID string, body map[string]any) (Message, error) {
	var m Message
	err := c.do(ctx, http.MethodPost, "/channels/"+url.PathEscape(channelID)+"/messages", nil, body, &m, "send message")
	return m, err
}

// Recent returns up to limit of the channel's latest messages, newest first.
func (c *Client) Recent(ctx context.Context, channelID string, limit int) ([]Message, error) {
	var out []Message
	err := c.do(ctx, http.MethodGet, "/channels/"+url.PathEscape(channelID)+"/messages", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &out, "read messages")
	return out, err
}
