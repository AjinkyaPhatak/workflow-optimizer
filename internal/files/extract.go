package files

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// MaxTextLength bounds extracted text (2 MB of characters).
const MaxTextLength = 2 << 20

// ErrUnsupported: no text can be extracted from this type.
var ErrUnsupported = errors.New("files: text extraction is not supported for this file type")

// Extracted is a file's text.
type Extracted struct {
	Text string
	// Pages is the page count for PDFs (0 otherwise).
	Pages int
	// Truncated reports that the text was cut at MaxTextLength.
	Truncated bool
}

// ExtractText reads the text of a PDF, Word (.docx), HTML or text-like
// file (plain text, Markdown, CSV, JSON, XML).
func ExtractText(f File, data []byte) (Extracted, error) {
	var (
		out Extracted
		err error
	)
	switch ct := f.ContentType; {
	case ct == "application/pdf":
		out, err = extractPDF(data)
	case ct == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		out.Text, err = extractDOCX(data)
	case ct == "text/html":
		out.Text = htmlText(string(data))
	case strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/xml":
		if !utf8.Valid(data) {
			return Extracted{}, fmt.Errorf("%w: the text is not UTF-8", ErrUnsupported)
		}
		out.Text = strings.TrimPrefix(string(data), string(rune(0xFEFF)))
	default:
		return Extracted{}, fmt.Errorf("%w (%s)", ErrUnsupported, ct)
	}
	if err != nil {
		return Extracted{}, err
	}
	out.Text = strings.ReplaceAll(out.Text, "\r\n", "\n")
	if len(out.Text) > MaxTextLength {
		cut := MaxTextLength
		for cut > 0 && !utf8.RuneStart(out.Text[cut]) {
			cut--
		}
		out.Text, out.Truncated = out.Text[:cut], true
	}
	return out, nil
}

func extractPDF(data []byte) (out Extracted, err error) {
	// The parser panics on some malformed files.
	defer func() {
		if r := recover(); r != nil {
			out, err = Extracted{}, fmt.Errorf("%w: the PDF could not be read", ErrInvalid)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Extracted{}, fmt.Errorf("%w: the PDF could not be read (it may be encrypted or damaged)", ErrInvalid)
	}
	out.Pages = r.NumPage()
	var b strings.Builder
	for i := 1; i <= out.Pages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue // one unreadable page does not lose the others
		}
		b.WriteString(strings.TrimSpace(text))
		b.WriteString("\n\n")
		if b.Len() > MaxTextLength {
			break
		}
	}
	out.Text = strings.TrimSpace(b.String())
	return out, nil
}

// extractDOCX reads word/document.xml: paragraphs become lines, tabs and
// breaks are kept.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: the Word document could not be read", ErrInvalid)
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", fmt.Errorf("%w: not a Word document", ErrInvalid)
	}
	rc, err := doc.Open()
	if err != nil {
		return "", fmt.Errorf("%w: the Word document could not be read", ErrInvalid)
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, 64<<20))
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: the Word document could not be read", ErrInvalid)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
		if b.Len() > MaxTextLength {
			break
		}
	}
	return strings.TrimSpace(b.String()), nil
}

var (
	dropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|template)\b.*?</(script|style|noscript|template)\s*>`)
	comments   = regexp.MustCompile(`(?s)<!--.*?-->`)
	blockTags  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer|blockquote|pre|table|ul|ol)\b[^>]*>`)
	tags       = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces     = regexp.MustCompile(`[ \t\f\v]+`)
	blankLines = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// htmlText is an HTML page's visible text.
func htmlText(s string) string {
	s = dropBlocks.ReplaceAllString(s, " ")
	s = comments.ReplaceAllString(s, " ")
	s = blockTags.ReplaceAllString(s, "\n")
	s = tags.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
