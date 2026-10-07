package gmail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"

	"workflow-optimizer/internal/integration"
)

// Message is an outgoing plain-text email.
type Message struct {
	To      []string
	Cc      []string
	Bcc     []string
	Subject string
	Body    string
	// InReplyTo and References make a reply thread correctly in every mail
	// client (RFC 5322 section 3.6.4).
	InReplyTo  string
	References string
}

// ParseAddresses parses a comma-separated address list ("a@x.com, Bob
// <b@y.com>"); empty input is an empty list.
func ParseAddresses(field, s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, integration.NewError(integration.KindInvalidRequest, field+" is not a valid list of email addresses")
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name == "" {
			out = append(out, a.Address)
		} else {
			out = append(out, a.String()) // quotes and encodes the name
		}
	}
	return out, nil
}

func headerSafe(field, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return integration.NewError(integration.KindInvalidRequest, field+" must be a single line")
	}
	return nil
}

// Build renders the message as RFC 5322 / MIME (UTF-8 text, base64 body).
// From is left to Gmail, which sets the authorized account's address. Bcc is
// kept in the raw message: Gmail delivers to it and removes the header.
func Build(m Message, now time.Time) ([]byte, error) {
	for _, h := range []struct{ name, v string }{{"subject", m.Subject}, {"In-Reply-To", m.InReplyTo}, {"References", m.References}} {
		if err := headerSafe(h.name, h.v); err != nil {
			return nil, err
		}
	}
	var b bytes.Buffer
	header := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", name, value)
		}
	}
	header("To", strings.Join(m.To, ", "))
	header("Cc", strings.Join(m.Cc, ", "))
	header("Bcc", strings.Join(m.Bcc, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("In-Reply-To", m.InReplyTo)
	header("References", m.References)
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="UTF-8"`)
	header("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	body := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(m.Body, "\r\n", "\n")))
	for len(body) > 76 {
		b.WriteString(body[:76] + "\r\n")
		body = body[76:]
	}
	b.WriteString(body + "\r\n")
	return b.Bytes(), nil
}

// Raw is the Gmail API "raw" field: the message, base64url-encoded.
func Raw(rfc822 []byte) string { return base64.URLEncoding.EncodeToString(rfc822) }

// ReplySubject prefixes "Re: " unless the subject already is a reply.
func ReplySubject(subject string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		return strings.TrimSpace(subject)
	}
	return "Re: " + strings.TrimSpace(subject)
}

// ReplyReferences appends the replied-to Message-ID to its References.
func ReplyReferences(references, messageID string) string {
	return strings.TrimSpace(strings.TrimSpace(references) + " " + messageID)
}
