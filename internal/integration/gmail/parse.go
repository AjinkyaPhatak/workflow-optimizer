package gmail

import (
	"encoding/base64"
	"html"
	"mime"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// apiMessage is the part of Gmail's users.messages resource this package
// reads. Everything else stays inside the client.
type apiMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	Snippet      string   `json:"snippet"`
	InternalDate string   `json:"internalDate"`
	LabelIDs     []string `json:"labelIds"`
	Payload      *apiPart `json:"payload"`
}

type apiPart struct {
	MimeType string      `json:"mimeType"`
	Filename string      `json:"filename"`
	Headers  []apiHeader `json:"headers"`
	Body     apiBody     `json:"body"`
	Parts    []apiPart   `json:"parts"`
}

type apiHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type apiBody struct {
	AttachmentID string `json:"attachmentId"`
	Size         int64  `json:"size"`
	Data         string `json:"data"`
}

func (p *apiPart) header(name string) string {
	if p == nil {
		return ""
	}
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

var wordDecoder = &mime.WordDecoder{}

// decodeHeader decodes RFC 2047 encoded words ("=?UTF-8?Q?...?=").
func decodeHeader(v string) string {
	if d, err := wordDecoder.DecodeHeader(v); err == nil {
		return d
	}
	return v
}

// addresses splits an address header into "Name <addr>" strings.
func addresses(v string) []string {
	if strings.TrimSpace(v) == "" {
		return []string{}
	}
	list, err := (&mail.AddressParser{WordDecoder: wordDecoder}).ParseList(v)
	if err != nil {
		return []string{decodeHeader(v)}
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, display(a))
	}
	return out
}

// display renders an address for workflow output: "addr" or "Name <addr>"
// (decoded, unquoted; not for use in outgoing headers).
func display(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.Name + " <" + a.Address + ">"
}

// sender is the From header for workflow output.
func sender(v string) string {
	if list := addresses(v); len(list) > 0 {
		return list[0]
	}
	return ""
}

// decodeData decodes Gmail's base64url body data (with or without padding).
func decodeData(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	return string(b), err == nil
}

// Attachment is attachment metadata (the content is not downloaded).
type Attachment struct {
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	AttachmentID string `json:"attachment_id"`
}

// walk collects the first text/plain and text/html bodies and the
// attachments of a MIME tree (multipart/alternative, mixed, related...).
func walk(p *apiPart, plain, htmlBody *string, attachments *[]Attachment) bool {
	if p == nil {
		return true
	}
	ok := true
	switch {
	case p.Filename != "":
		*attachments = append(*attachments, Attachment{Filename: p.Filename, MimeType: p.MimeType, Size: p.Body.Size, AttachmentID: p.Body.AttachmentID})
	case strings.HasPrefix(p.MimeType, "multipart/"):
		for i := range p.Parts {
			ok = walk(&p.Parts[i], plain, htmlBody, attachments) && ok
		}
	case p.MimeType == "text/plain" && *plain == "":
		*plain, ok = decodeData(p.Body.Data)
	case p.MimeType == "text/html" && *htmlBody == "":
		*htmlBody, ok = decodeData(p.Body.Data)
	}
	return ok
}

var (
	blockTags = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6])\b[^>]*>`)
	allTags   = regexp.MustCompile(`(?s)<[^>]*>`)
	dropped   = regexp.MustCompile(`(?is)<(style|script|head)\b.*?</(style|script|head)>`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is a plain-text rendering of an HTML-only email body.
func htmlToText(s string) string {
	s = dropped.ReplaceAllString(s, "")
	s = blockTags.ReplaceAllString(s, "\n")
	s = html.UnescapeString(allTags.ReplaceAllString(s, ""))
	return strings.TrimSpace(blankRuns.ReplaceAllString(s, "\n\n"))
}

func timestamp(internalDate string, dateHeader string) string {
	if ms, err := strconv.ParseInt(internalDate, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	if t, err := mail.ParseDate(dateHeader); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// readOutput is gmail.read's normalized message.
func readOutput(m apiMessage) (map[string]any, bool) {
	var plain, htmlBody string
	attachments := []Attachment{}
	ok := walk(m.Payload, &plain, &htmlBody, &attachments)
	body := plain
	if body == "" && htmlBody != "" {
		body = htmlToText(htmlBody)
	}
	recipients := append(addresses(m.Payload.header("To")), addresses(m.Payload.header("Cc"))...)
	atts := make([]any, 0, len(attachments))
	for _, a := range attachments {
		atts = append(atts, map[string]any{"filename": a.Filename, "mime_type": a.MimeType, "size": float64(a.Size), "attachment_id": a.AttachmentID})
	}
	return map[string]any{
		"message_id":  m.ID,
		"thread_id":   m.ThreadID,
		"sender":      sender(m.Payload.header("From")),
		"recipients":  toAny(recipients),
		"subject":     decodeHeader(m.Payload.header("Subject")),
		"body":        body,
		"timestamp":   timestamp(m.InternalDate, m.Payload.header("Date")),
		"attachments": atts,
	}, ok
}

// summary is one gmail.search result.
func summary(m apiMessage) map[string]any {
	return map[string]any{
		"message_id": m.ID,
		"thread_id":  m.ThreadID,
		"sender":     sender(m.Payload.header("From")),
		"subject":    decodeHeader(m.Payload.header("Subject")),
		"snippet":    html.UnescapeString(m.Snippet),
		"timestamp":  timestamp(m.InternalDate, m.Payload.header("Date")),
		"unread":     contains(m.LabelIDs, "UNREAD"),
	}
}

func toAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
