// Package gmail is the Gmail integration (Phase C3): five actions that are
// ordinary workflow nodes on the C1 integration framework, authenticated
// with Google connected accounts (C2).
//
//	gmail.<action> node -> integration.ActionNode
//	    -> credential.Resolver (oauth.TokenManager: decrypt, refresh)
//	    -> valid Google access token -> Connector -> Client -> Gmail REST API
//
// Nothing here knows about OAuth flows, token refresh, persistence or
// retries: the token manager hands the client a valid token, and failures
// are classified as integration errors for the Phase 10 engine.
//
// Output shapes (stable, documented in README):
//
//	search:       messages [{message_id, thread_id, sender, subject, snippet, timestamp, unread}], count
//	read:         message_id, thread_id, sender, recipients [], subject, body, timestamp,
//	              attachments [{filename, mime_type, size, attachment_id}]
//	create_draft: draft_id, message_id, status "draft"
//	send, reply:  message_id, thread_id, status "sent"
package gmail

import (
	"context"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID; its actions are gmail.search, gmail.read,
// gmail.create_draft, gmail.send and gmail.reply.
const ID = "gmail"

// Provider is the OAuth provider whose connected accounts Gmail uses.
const Provider = "google"

// MaxSearchResults bounds gmail.search's max_results.
const MaxSearchResults = 50

func str(name, desc string) node.PortDefinition {
	return node.NewPortDefinition(name, node.ValueTypeString, false, desc)
}

func field(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func recipientsFields() []node.ConfigField {
	return []node.ConfigField{
		field("cc", "Cc", "Comma-separated addresses to copy. {{variables}} are filled in.", false),
		field("bcc", "Bcc", "Comma-separated addresses to blind-copy. {{variables}} are filled in.", false),
	}
}

func sentOutputs() []node.PortDefinition {
	return []node.PortDefinition{
		node.NewPortDefinition("message_id", node.ValueTypeString, true, "ID of the sent message"),
		node.NewPortDefinition("thread_id", node.ValueTypeString, true, "ID of its conversation"),
		node.NewPortDefinition("status", node.ValueTypeString, true, `"sent"`),
	}
}

// Integration is Gmail's metadata.
func Integration() integration.Integration {
	composeFields := append([]node.ConfigField{
		field("to", "To", "Comma-separated recipient addresses. {{variables}} are filled in.", false),
	}, recipientsFields()...)
	composeFields = append(composeFields,
		field("subject", "Subject", "{{variables}} are filled in.", false),
		field("body", "Body", "Plain-text message. Used when the body input is not connected; {{variables}} are filled in.", false).WithMultiline(),
	)
	sendFields := append([]node.ConfigField{}, composeFields...)
	sendFields[0] = field("to", "To", "Comma-separated recipient addresses. {{variables}} are filled in.", true)
	return integration.Integration{
		ID: ID, Name: "Gmail", Category: "Google", Icon: "gmail",
		Description: "Search, read, draft and send email with a connected Google account.",
		DocsURL:     "https://developers.google.com/gmail/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "search", Name: "Search Emails", SideEffects: node.SideEffectsNone,
				Description: "Finds messages with a Gmail search query (e.g. from:someone@example.com is:unread) and returns their sender, subject and snippet.",
				Inputs:      []node.PortDefinition{str("query", "Search query (overrides the Query setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("messages", node.ValueTypeArray, true, "Matching messages: message_id, thread_id, sender, subject, snippet, timestamp, unread"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of messages returned"),
				},
				Config: []node.ConfigField{
					field("query", "Query", "Gmail search syntax, e.g. from:foo@example.com subject:invoice is:unread. Empty matches all mail.", false),
					node.NewConfigField("max_results", node.ValueTypeNumber, false, 10.0, "How many messages to return at most.").
						WithLabel("Max results").WithRange(node.Float(1), node.Float(MaxSearchResults), node.Float(1)),
				},
			},
			{
				ID: "read", Name: "Read Email", SideEffects: node.SideEffectsNone,
				Description: "Reads one message: sender, recipients, subject, plain-text body, time and attachment names.",
				Inputs:      []node.PortDefinition{str("message_id", "Message ID, e.g. from Search Emails (overrides the setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("message_id", node.ValueTypeString, true, "Message ID"),
					node.NewPortDefinition("thread_id", node.ValueTypeString, true, "Conversation ID"),
					node.NewPortDefinition("sender", node.ValueTypeString, true, "From address"),
					node.NewPortDefinition("recipients", node.ValueTypeArray, true, "To and Cc addresses"),
					node.NewPortDefinition("subject", node.ValueTypeString, true, "Subject"),
					node.NewPortDefinition("body", node.ValueTypeString, true, "Plain-text body (HTML-only mail is converted to text)"),
					node.NewPortDefinition("timestamp", node.ValueTypeString, true, "When it was received (RFC 3339)"),
					node.NewPortDefinition("attachments", node.ValueTypeArray, true, "Attachment metadata: filename, mime_type, size, attachment_id"),
				},
				Config: []node.ConfigField{field("message_id", "Message ID", "Used when the message_id input is not connected; {{variables}} are filled in.", false)},
			},
			{
				// Gmail has no idempotency key for drafts: a repeated call
				// creates a second draft, so it is not retried after a
				// failure that may have applied.
				ID: "create_draft", Name: "Create Draft", SideEffects: node.SideEffectsUnsafe,
				Description: "Saves a draft in the account's Drafts folder without sending it.",
				Inputs:      []node.PortDefinition{str("body", "Message body (overrides the Body setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("draft_id", node.ValueTypeString, true, "Draft ID"),
					node.NewPortDefinition("message_id", node.ValueTypeString, true, "ID of the draft's message"),
					node.NewPortDefinition("status", node.ValueTypeString, true, `"draft"`),
				},
				Config: composeFields,
			},
			{
				ID: "send", Name: "Send Email", SideEffects: node.SideEffectsUnsafe,
				Description: "Sends an email from the connected account. Not retried automatically after a failure that may have sent it.",
				Inputs:      []node.PortDefinition{str("body", "Message body (overrides the Body setting)")},
				Outputs:     sentOutputs(),
				Config:      sendFields,
			},
			{
				ID: "reply", Name: "Reply", SideEffects: node.SideEffectsUnsafe,
				Description: "Replies to a message in its conversation (to its sender, with Re: subject and threading headers).",
				Inputs: []node.PortDefinition{
					str("message_id", "Message to reply to (overrides the setting)"),
					str("body", "Reply text (overrides the Body setting)"),
				},
				Outputs: sentOutputs(),
				Config: append([]node.ConfigField{
					field("message_id", "Message ID", "The message to reply to. Used when the message_id input is not connected.", false),
					field("body", "Body", "Plain-text reply. Used when the body input is not connected; {{variables}} are filled in.", false).WithMultiline(),
				}, recipientsFields()...),
			},
		},
	}
}

// text is the connected input of that name, else the (resolved) config.
func text(in node.NodeInput, name string) string {
	if v, ok := in.GetPort(name); ok {
		if s, ok := v.String(); ok {
			return s
		}
	}
	s, _ := in.GetStringConfig(name)
	return s
}

func invalid(msg string) error { return integration.NewError(integration.KindInvalidRequest, msg) }

func recipients(in node.NodeInput, names ...string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, n := range names {
		list, err := ParseAddresses(n, text(in, n))
		if err != nil {
			return nil, err
		}
		out[n] = list
	}
	return out, nil
}

func search(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	max := 10
	if f, ok := in.GetNumberConfig("max_results"); ok {
		if f < 1 || f > MaxSearchResults || f != float64(int(f)) {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "max_results must be a whole number from 1 to 50", false)
		}
		max = int(f)
	}
	msgs, err := c.Search(ctx, strings.TrimSpace(text(in, "query")), max)
	if err != nil {
		return node.NodeOutput{}, err
	}
	list := make([]any, 0, len(msgs))
	for _, m := range msgs {
		list = append(list, summary(m))
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("messages", node.NewArrayValue(list))
	out.SetPort("count", node.NewNumberValue(float64(len(list))))
	return out, nil
}

func read(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	id := strings.TrimSpace(text(in, "message_id"))
	if id == "" {
		return node.NodeOutput{}, invalid("a message_id is required")
	}
	m, err := c.Get(ctx, id, "full")
	if err != nil {
		return node.NodeOutput{}, err
	}
	fields, ok := readOutput(m)
	if !ok {
		return node.NodeOutput{}, integration.NewError(integration.KindMalformedResponse, "read message: the message body could not be decoded")
	}
	out := node.NewNodeOutput(nil)
	for k, v := range fields {
		switch t := v.(type) {
		case string:
			out.SetPort(k, node.NewStringValue(t))
		case []any:
			out.SetPort(k, node.NewArrayValue(t))
		}
	}
	return out, nil
}

func compose(in node.NodeInput, requireTo bool) (Message, error) {
	r, err := recipients(in, "to", "cc", "bcc")
	if err != nil {
		return Message{}, err
	}
	if requireTo && len(r["to"]) == 0 {
		return Message{}, invalid("at least one recipient (to) is required")
	}
	return Message{To: r["to"], Cc: r["cc"], Bcc: r["bcc"], Subject: strings.TrimSpace(text(in, "subject")), Body: text(in, "body")}, nil
}

func createDraft(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	m, err := compose(in, false)
	if err != nil {
		return node.NodeOutput{}, err
	}
	raw, err := Build(m, time.Now())
	if err != nil {
		return node.NodeOutput{}, err
	}
	d, err := c.CreateDraft(ctx, raw)
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("draft_id", node.NewStringValue(d.ID))
	out.SetPort("message_id", node.NewStringValue(d.Message.ID))
	out.SetPort("status", node.NewStringValue("draft"))
	return out, nil
}

func sent(s sentMessage) node.NodeOutput {
	out := node.NewNodeOutput(nil)
	out.SetPort("message_id", node.NewStringValue(s.ID))
	out.SetPort("thread_id", node.NewStringValue(s.ThreadID))
	out.SetPort("status", node.NewStringValue("sent"))
	return out
}

func send(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	m, err := compose(in, true)
	if err != nil {
		return node.NodeOutput{}, err
	}
	raw, err := Build(m, time.Now())
	if err != nil {
		return node.NodeOutput{}, err
	}
	s, err := c.Send(ctx, raw, "")
	if err != nil {
		return node.NodeOutput{}, err
	}
	return sent(s), nil
}

// reply answers a message in its thread: to its Reply-To (else From), with
// "Re: <subject>", In-Reply-To and References.
func reply(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	id := strings.TrimSpace(text(in, "message_id"))
	if id == "" {
		return node.NodeOutput{}, invalid("a message_id is required")
	}
	r, err := recipients(in, "cc", "bcc")
	if err != nil {
		return node.NodeOutput{}, err
	}
	orig, err := c.Get(ctx, id, "metadata")
	if err != nil {
		return node.NodeOutput{}, err
	}
	to := orig.Payload.header("Reply-To")
	if strings.TrimSpace(to) == "" {
		to = orig.Payload.header("From")
	}
	toList, err := ParseAddresses("the original sender", to)
	if err != nil || len(toList) == 0 {
		return node.NodeOutput{}, invalid("the message has no sender to reply to")
	}
	msgID := strings.TrimSpace(orig.Payload.header("Message-ID"))
	raw, err := Build(Message{
		To: toList, Cc: r["cc"], Bcc: r["bcc"], Subject: ReplySubject(decodeHeader(orig.Payload.header("Subject"))), Body: text(in, "body"),
		InReplyTo: msgID, References: ReplyReferences(orig.Payload.header("References"), msgID),
	}, time.Now())
	if err != nil {
		return node.NodeOutput{}, err
	}
	s, err := c.Send(ctx, raw, orig.ThreadID)
	if err != nil {
		return node.NodeOutput{}, err
	}
	return sent(s), nil
}

// Connector builds a Gmail client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Gmail with its executable nodes. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{
		"search": search, "read": read, "create_draft": createDraft, "send": send, "reply": reply,
	}
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
