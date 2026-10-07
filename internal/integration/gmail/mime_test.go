package gmail

import (
	"encoding/base64"
	"net/mail"
	"strings"
	"testing"
	"time"

	"workflow-optimizer/internal/integration"
)

func parseBuilt(t *testing.T, m Message) (*mail.Message, string) {
	t.Helper()
	raw, err := Build(m, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("not RFC 5322: %v\n%s", err, raw)
	}
	b, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(readAll(t, msg), "\r\n", ""))
	return msg, string(b)
}

func readAll(t *testing.T, m *mail.Message) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := m.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

func TestBuildPlainMessage(t *testing.T) {
	body := strings.Repeat("Hello Grüße, ", 20) + "\nSecond line"
	msg, decoded := parseBuilt(t, Message{To: []string{"Ada <ada@example.test>", "b@example.test"}, Cc: []string{"c@example.test"},
		Bcc: []string{"d@example.test"}, Subject: "Invoice ✓ October", Body: body})
	h := msg.Header
	if h.Get("To") != "Ada <ada@example.test>, b@example.test" || h.Get("Cc") != "c@example.test" || h.Get("Bcc") != "d@example.test" {
		t.Fatalf("recipients: %v", h)
	}
	if subj, _ := new(mail.Header).Date(); subj.IsZero() {
		_ = subj
	}
	dec, _ := (&mime_decoder{}).decode(h.Get("Subject"))
	if dec != "Invoice ✓ October" || !strings.HasPrefix(h.Get("Subject"), "=?utf-8?q?") {
		t.Fatalf("subject must be RFC 2047 encoded: %q -> %q", h.Get("Subject"), dec)
	}
	if h.Get("MIME-Version") != "1.0" || h.Get("Content-Type") != `text/plain; charset="UTF-8"` || h.Get("Content-Transfer-Encoding") != "base64" {
		t.Fatalf("MIME headers: %v", h)
	}
	if h.Get("From") != "" || h.Get("In-Reply-To") != "" {
		t.Fatal("From is set by Gmail; no reply headers on a new message")
	}
	if decoded != body {
		t.Fatalf("body round trip: %q", decoded)
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	for _, m := range []Message{
		{Subject: "Hi\r\nBcc: victim@example.test"},
		{Subject: "Hi\nX-Evil: 1"},
		{InReplyTo: "<a@b>\r\nBcc: x@y"},
	} {
		if _, err := Build(m, time.Now()); err == nil {
			t.Fatalf("injection accepted: %+v", m)
		}
	}
	if _, err := ParseAddresses("to", "a@example.test\r\nBcc: x@example.test"); err == nil {
		t.Fatal("an address list with a line break is rejected")
	}
	if _, err := ParseAddresses("to", "not an address"); err == nil {
		t.Fatal("invalid address accepted")
	}
	var ie *integration.Error
	_, err := ParseAddresses("cc", "@@")
	if e, ok := err.(*integration.Error); !ok || e.Kind != integration.KindInvalidRequest {
		t.Fatalf("invalid request expected: %v %v", err, ie)
	}
	if l, err := ParseAddresses("to", " "); err != nil || len(l) != 0 {
		t.Fatal("empty list")
	}
}

func TestReplyHeaders(t *testing.T) {
	msg, _ := parseBuilt(t, Message{To: []string{"ada@example.test"}, Subject: ReplySubject("Invoice"), Body: "Thanks",
		InReplyTo: "<orig@mail.example>", References: ReplyReferences("<first@mail.example>", "<orig@mail.example>")})
	if msg.Header.Get("In-Reply-To") != "<orig@mail.example>" || msg.Header.Get("References") != "<first@mail.example> <orig@mail.example>" {
		t.Fatalf("reply headers: %v", msg.Header)
	}
	if ReplySubject("Invoice") != "Re: Invoice" || ReplySubject("RE: Invoice") != "RE: Invoice" || ReplySubject(" re: x ") != "re: x" {
		t.Fatal("Re: prefix")
	}
	if ReplyReferences("", "<a@b>") != "<a@b>" {
		t.Fatal("references of a first reply")
	}
}

// mime_decoder decodes RFC 2047 words for the test.
type mime_decoder struct{}

func (mime_decoder) decode(s string) (string, error) { return wordDecoder.DecodeHeader(s) }

func part(mimeType, filename, data string, parts ...apiPart) apiPart {
	p := apiPart{MimeType: mimeType, Filename: filename, Body: apiBody{Data: base64.URLEncoding.EncodeToString([]byte(data)), Size: int64(len(data))}, Parts: parts}
	if filename != "" {
		p.Body = apiBody{AttachmentID: "att-1", Size: int64(len(data))}
	}
	return p
}

func TestReadOutputNormalization(t *testing.T) {
	headers := []apiHeader{{"From", "=?UTF-8?Q?Ren=C3=A9?= <rene@example.test>"}, {"To", "a@example.test, Bob <b@example.test>"},
		{"Cc", "c@example.test"}, {"Subject", "=?UTF-8?B?w6l0w6k=?="}, {"Date", "Tue, 06 Oct 2026 10:00:00 +0000"}}

	plain := apiMessage{ID: "m1", ThreadID: "t1", InternalDate: "1791370800000", Payload: &apiPart{MimeType: "text/plain", Headers: headers,
		Body: apiBody{Data: base64.URLEncoding.EncodeToString([]byte("Hello\nworld"))}}}
	out, ok := readOutput(plain)
	if !ok || out["body"] != "Hello\nworld" || out["sender"] != "René <rene@example.test>" || out["subject"] != "été" ||
		out["timestamp"] != "2026-10-07T11:00:00Z" || len(out["attachments"].([]any)) != 0 {
		t.Fatalf("plain: %v", out)
	}

	if r := out["recipients"].([]any); len(r) != 3 || r[0] != "a@example.test" || r[1] != "Bob <b@example.test>" || r[2] != "c@example.test" {
		t.Fatalf("recipients: %v", r)
	}

	multi := apiMessage{ID: "m2", ThreadID: "t2", Payload: &apiPart{MimeType: "multipart/mixed", Headers: headers, Parts: []apiPart{
		part("multipart/alternative", "", "", part("text/plain", "", "Plain version"), part("text/html", "", "<p>HTML <b>version</b></p>")),
		part("application/pdf", "invoice.pdf", "%PDF-1.7 ..."),
	}}}
	out, ok = readOutput(multi)
	atts := out["attachments"].([]any)
	if !ok || out["body"] != "Plain version" || len(atts) != 1 {
		t.Fatalf("multipart: %v", out)
	}
	if a := atts[0].(map[string]any); a["filename"] != "invoice.pdf" || a["mime_type"] != "application/pdf" || a["attachment_id"] != "att-1" || a["size"] != 12.0 {
		t.Fatalf("attachment: %v", a)
	}
	if out["timestamp"] != "2026-10-06T10:00:00Z" {
		t.Fatalf("Date header fallback: %v", out["timestamp"])
	}

	htmlOnly := apiMessage{ID: "m3", Payload: &apiPart{MimeType: "multipart/alternative", Headers: headers, Parts: []apiPart{
		part("text/html", "", "<html><head><style>p{}</style></head><body><p>Dear &amp; <i>valued</i> customer,</p><p>Thanks</p></body></html>"),
	}}}
	if out, _ := readOutput(htmlOnly); out["body"] != "Dear & valued customer,\nThanks" {
		t.Fatalf("html-only body: %q", out["body"])
	}

	// Gmail sometimes omits base64 padding.
	unpadded := apiMessage{ID: "m4", Payload: &apiPart{MimeType: "text/plain", Body: apiBody{Data: base64.RawURLEncoding.EncodeToString([]byte("ab"))}}}
	if out, ok := readOutput(unpadded); !ok || out["body"] != "ab" {
		t.Fatalf("unpadded: %v", out)
	}
	broken := apiMessage{ID: "m5", Payload: &apiPart{MimeType: "text/plain", Body: apiBody{Data: "!!!not base64!!!"}}}
	if _, ok := readOutput(broken); ok {
		t.Fatal("undecodable body is reported")
	}
}
