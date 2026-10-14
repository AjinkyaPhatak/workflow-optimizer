// Package discord is the Discord integration (Phase C6): two actions on the
// C1 integration framework. A Discord connected account (C2, provider
// "discord") is a server the application's bot was added to; actions run
// with the bot token from configuration and are confined to that server:
// every channel is checked to belong to it before anything is read or sent,
// so one workspace's workflows can never reach another workspace's server.
//
//	send_message:  message_id, message_ids [], channel_id, url
//	read_messages: messages [{message_id, author, author_id, is_bot, content, timestamp,
//	               url, attachments []}] (oldest first), count, text, message_id (latest)
package discord

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: discord.send_message, discord.read_messages.
const ID = "discord"

// Provider is the OAuth provider whose connected accounts Discord uses.
const Provider = "discord"

// Discord's limits.
const (
	MaxContent   = 2000 // characters per message
	MaxParts     = 10   // messages one send may split long text into
	MaxRead      = 100
	maxEmbedText = 4096
)

func str(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func channelField() node.ConfigField {
	return str("channel", "Channel", "The channel's name (e.g. general), its link (right-click the channel, Copy Link) or its ID. It must be in the connected server.", true)
}

// Integration is Discord's metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Discord", Category: "Discord", Icon: "discord",
		Description: "Send messages to and read recent messages from a server's channels, with the bot added to that server.",
		DocsURL:     "https://docs.discord.com/developers/resources/message",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				// Discord deduplicates a repeated send only for a few minutes
				// (nonce), so a send is not retried after a failure that may
				// have applied.
				ID: "send_message", Name: "Send Message", SideEffects: node.SideEffectsUnsafe,
				Description: "Posts a message (and optionally an embed) to a channel. Text over 2000 characters is split into several messages.",
				Inputs:      []node.PortDefinition{node.NewPortDefinition("content", node.ValueTypeJSON, false, "Any upstream data; text is sent as the message (overriding the Message setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("message_id", node.ValueTypeString, true, "ID of the (first) message sent"),
					node.NewPortDefinition("message_ids", node.ValueTypeArray, true, "IDs of all messages sent (long text is split)"),
					node.NewPortDefinition("channel_id", node.ValueTypeString, true, "Channel ID"),
					node.NewPortDefinition("url", node.ValueTypeString, true, "Link to the (first) message"),
				},
				Config: []node.ConfigField{
					channelField(),
					str("content", "Message", "Text to send (Discord markdown). {{variables}} are filled in.", false).WithMultiline(),
					str("embed_title", "Embed title", "Optional card under the message. {{variables}} are filled in.", false),
					str("embed_description", "Embed text", "{{variables}} are filled in.", false).WithMultiline(),
					str("embed_url", "Embed link", "Makes the embed title a link.", false),
					str("embed_color", "Embed color", "Hex color, e.g. #5865F2.", false),
					node.NewConfigField("allow_mentions", node.ValueTypeBoolean, false, false,
						"Let @everyone, @here, role and user mentions in the text notify people. Off: mentions are shown but notify nobody (safer for text from emails or AI).").
						WithLabel("Allow mentions"),
				},
			},
			{
				ID: "read_messages", Name: "Read Messages", SideEffects: node.SideEffectsNone,
				Description: "Reads a channel's latest messages, oldest first, plus a ready-to-use transcript (e.g. for an LLM summary).",
				Inputs:      []node.PortDefinition{node.NewPortDefinition("channel", node.ValueTypeJSON, false, "Any upstream data; text is used as the channel (overriding the Channel setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("messages", node.ValueTypeArray, true, "Messages, oldest first: message_id, author, author_id, is_bot, content, timestamp, url, attachments"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of messages returned"),
					node.NewPortDefinition("text", node.ValueTypeString, true, "Transcript: one \"author: content\" line per message, oldest first"),
					node.NewPortDefinition("message_id", node.ValueTypeString, true, "ID of the latest message (empty when there is none)"),
				},
				Config: []node.ConfigField{
					channelField(),
					node.NewConfigField("limit", node.ValueTypeNumber, false, 20.0, "How many of the latest messages to read.").
						WithLabel("Messages").WithRange(node.Float(1), node.Float(MaxRead), node.Float(1)),
				},
			},
		},
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func invalid(msg string) error { return integration.NewError(integration.KindInvalidRequest, msg) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

func textOrPort(in node.NodeInput, port, setting string) string {
	if v, ok := in.GetPort(port); ok {
		if s, ok := v.String(); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	s, _ := in.GetStringConfig(setting)
	return s
}

var (
	snowflake   = regexp.MustCompile(`^\d{15,22}$`)
	channelLink = regexp.MustCompile(`^https?://(?:(?:ptb|canary)\.)?discord(?:app)?\.com/channels/(\d{15,22})/(\d{15,22})(?:/\d{15,22})?/?$`)
)

func outside() error {
	return integration.NewError(integration.KindPermissionDenied, "that channel is not in the connected Discord server")
}

// channel resolves a channel name, link or ID to a channel of the client's
// server; any other server's channel is refused.
func channel(ctx context.Context, c *Client, raw string) (Channel, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Channel{}, configError("set the Channel")
	}
	id := ""
	switch {
	case snowflake.MatchString(raw):
		id = raw
	case channelLink.MatchString(raw):
		m := channelLink.FindStringSubmatch(raw)
		if m[1] != c.Guild() {
			return Channel{}, outside()
		}
		id = m[2]
	case strings.Contains(raw, "://"):
		return Channel{}, invalid("the channel link must look like https://discord.com/channels/<server>/<channel>")
	}
	if id != "" {
		ch, err := c.GetChannel(ctx, id)
		if err != nil {
			return Channel{}, err
		}
		if ch.GuildID != c.Guild() {
			return Channel{}, outside()
		}
		return ch, nil
	}
	name := strings.ToLower(strings.TrimPrefix(raw, "#"))
	all, err := c.Channels(ctx)
	if err != nil {
		return Channel{}, err
	}
	var names []string
	for _, ch := range all {
		if ch.Type == 4 { // a category, not a channel
			continue
		}
		if strings.ToLower(ch.Name) == name {
			ch.GuildID = c.Guild()
			return ch, nil
		}
		names = append(names, ch.Name)
	}
	if len(names) > 20 {
		names = append(names[:20], "…")
	}
	return Channel{}, integration.NewError(integration.KindNotFound,
		fmt.Sprintf("the connected server has no channel named %q; its channels include: %s", name, strings.Join(names, ", ")))
}

// split cuts text into parts of at most MaxContent characters, preferring
// line breaks, then spaces.
func split(s string) []string {
	var parts []string
	for utf8.RuneCountInString(s) > MaxContent {
		r := []rune(s)
		cut := MaxContent
		head := string(r[:cut])
		if i := strings.LastIndex(head, "\n"); i > MaxContent/2 {
			cut = utf8.RuneCountInString(head[:i]) + 1
		} else if i := strings.LastIndex(head, " "); i > MaxContent/2 {
			cut = utf8.RuneCountInString(head[:i]) + 1
		}
		parts = append(parts, strings.TrimRight(string(r[:cut]), "\n "))
		s = string(r[cut:])
	}
	if strings.TrimSpace(s) != "" {
		parts = append(parts, strings.TrimRight(s, "\n "))
	}
	return parts
}

var hexColor = regexp.MustCompile(`^#?([0-9a-fA-F]{6})$`)

func embed(in node.NodeInput) (map[string]any, error) {
	title, desc, link, color := text(in, "embed_title"), text(in, "embed_description"), text(in, "embed_url"), text(in, "embed_color")
	if title == "" && desc == "" {
		if link != "" || color != "" {
			return nil, configError("an embed needs a title or text")
		}
		return nil, nil
	}
	if utf8.RuneCountInString(title) > 256 || utf8.RuneCountInString(desc) > maxEmbedText {
		return nil, invalid("the embed is too long (title: 256 characters, text: 4096)")
	}
	e := map[string]any{}
	if title != "" {
		e["title"] = title
	}
	if desc != "" {
		e["description"] = desc
	}
	if link != "" {
		if !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
			return nil, configError("the embed link must start with https://")
		}
		e["url"] = link
	}
	if color != "" {
		m := hexColor.FindStringSubmatch(color)
		if m == nil {
			return nil, configError("the embed color must be a hex color like #5865F2")
		}
		n, _ := strconv.ParseInt(m[1], 16, 32)
		e["color"] = n
	}
	return e, nil
}

// nonce derives Discord's per-message deduplication key (at most 25
// characters) from the node's idempotency key.
func nonce(key string, part int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("discord.send_message:%s:%d", key, part)))
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:25]
}

func messageURL(guild, channel, id string) string {
	return "https://discord.com/channels/" + guild + "/" + channel + "/" + id
}

func sendMessage(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	content := textOrPort(in, "content", "content")
	e, err := embed(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	parts := split(content)
	if len(parts) == 0 && e == nil {
		return node.NodeOutput{}, configError("set a Message or an embed to send")
	}
	if len(parts) > MaxParts {
		return node.NodeOutput{}, invalid(fmt.Sprintf("the message is too long: at most %d characters", MaxParts*MaxContent))
	}
	if len(parts) == 0 {
		parts = []string{""}
	}
	ch, err := channel(ctx, c, text(in, "channel"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	mentions := map[string]any{"parse": []string{}}
	if b, _ := in.GetBooleanConfig("allow_mentions"); b {
		mentions = map[string]any{"parse": []string{"users", "roles", "everyone"}}
	}
	ids := []any{}
	for i, p := range parts {
		body := map[string]any{"allowed_mentions": mentions}
		if p != "" {
			body["content"] = p
		}
		if e != nil && i == len(parts)-1 {
			body["embeds"] = []any{e}
		}
		if in.IdempotencyKey != "" {
			body["nonce"], body["enforce_nonce"] = nonce(in.IdempotencyKey, i), true
		}
		m, err := c.Send(ctx, ch.ID, body)
		if err != nil {
			return node.NodeOutput{}, err
		}
		ids = append(ids, m.ID)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("message_id", node.NewStringValue(ids[0].(string)))
	out.SetPort("message_ids", node.NewArrayValue(ids))
	out.SetPort("channel_id", node.NewStringValue(ch.ID))
	out.SetPort("url", node.NewStringValue(messageURL(c.Guild(), ch.ID, ids[0].(string))))
	return out, nil
}

func readMessages(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	limit := 20
	if f, ok := in.GetNumberConfig("limit"); ok {
		if f < 1 || f > MaxRead || f != float64(int(f)) {
			return node.NodeOutput{}, configError(fmt.Sprintf("limit must be a whole number from 1 to %d", MaxRead))
		}
		limit = int(f)
	}
	ch, err := channel(ctx, c, textOrPort(in, "channel", "channel"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	recent, err := c.Recent(ctx, ch.ID, limit)
	if err != nil {
		return node.NodeOutput{}, err
	}
	msgs := make([]any, 0, len(recent))
	var transcript strings.Builder
	for i := len(recent) - 1; i >= 0; i-- { // Discord answers newest first
		m := recent[i]
		author := m.Author.GlobalName
		if author == "" {
			author = m.Author.Username
		}
		files := []any{}
		for _, a := range m.Attachments {
			files = append(files, a.Filename)
		}
		msgs = append(msgs, map[string]any{
			"message_id": m.ID, "author": author, "author_id": m.Author.ID, "is_bot": m.Author.Bot, "content": m.Content,
			"timestamp": m.Timestamp, "url": messageURL(c.Guild(), ch.ID, m.ID), "attachments": files,
		})
		fmt.Fprintf(&transcript, "%s: %s\n", author, strings.ReplaceAll(m.Content, "\n", " "))
	}
	latest := ""
	if len(recent) > 0 {
		latest = recent[0].ID
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("messages", node.NewArrayValue(msgs))
	out.SetPort("count", node.NewNumberValue(float64(len(msgs))))
	out.SetPort("text", node.NewStringValue(strings.TrimRight(transcript.String(), "\n")))
	out.SetPort("message_id", node.NewStringValue(latest))
	return out, nil
}

// Connector builds a client confined to the connected server (the
// credential's account ID), acting with the bot token.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		if o.BotToken.Empty() {
			return nil, configError("Discord is not configured on this server (DISCORD_BOT_TOKEN)")
		}
		if !snowflake.MatchString(cred.AccountID) {
			return nil, node.NewNodeError(integration.ErrCodeCredentialInvalid, "the Discord account names no server; reconnect it", false)
		}
		return NewClient(o, cred.AccountID), nil
	})
}

// Module returns Discord with its executable nodes. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{"send_message": sendMessage, "read_messages": readMessages}
	nodes := map[string]node.Node{}
	for id, h := range handlers {
		n, err := integration.NewActionNode(in, id, creds, Connector(o), h)
		if err != nil {
			return integration.Module{}, err
		}
		nodes[id] = n
	}
	return integration.Module{Integration: in, Nodes: nodes}, nil
}
