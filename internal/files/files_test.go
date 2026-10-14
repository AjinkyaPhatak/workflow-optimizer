package files_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
)

// PDF builds a minimal valid PDF with one text line per page.
func PDF(pages ...string) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n")
	kids := []string{}
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+2*i))
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	for i, text := range pages {
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

// DOCX builds a minimal Word document with the given paragraphs.
func DOCX(paragraphs ...string) []byte {
	var body strings.Builder
	for _, p := range paragraphs {
		fmt.Fprintf(&body, `<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, p)
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("word/document.xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%s<w:p><w:r><w:t>A</w:t><w:tab/><w:t>B</w:t><w:br/><w:t>C</w:t></w:r></w:p></w:body></w:document>`, body.String())
	_ = z.Close()
	return b.Bytes()
}

func extract(t *testing.T, name string, data []byte) files.Extracted {
	t.Helper()
	x, err := files.ExtractText(files.File{Name: name, ContentType: files.ContentType(name, data)}, data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return x
}

func TestExtractText(t *testing.T) {
	if x := extract(t, "report.pdf", PDF("Quarterly revenue grew", "Second page")); !strings.Contains(x.Text, "Quarterly revenue grew") ||
		!strings.Contains(x.Text, "Second page") || x.Pages != 2 {
		t.Fatalf("pdf: %+v", x)
	}
	if x := extract(t, "notes.docx", DOCX("Meeting notes", "Action items")); x.Text != "Meeting notes\nAction items\nA\tB\nC" {
		t.Fatalf("docx: %q", x.Text)
	}
	page := `<html><head><style>body{}</style><script>alert(1)</script></head><body><h1>Title</h1><p>Hello &amp; welcome</p><!-- hidden --><ul><li>One</li><li>Two</li></ul></body></html>`
	if x := extract(t, "page.html", []byte(page)); x.Text != "Title\n\nHello & welcome\n\nOne\n\nTwo" {
		t.Fatalf("html: %q", x.Text)
	}
	if x := extract(t, "data.csv", []byte("\ufeffa,b\r\n1,2\r\n")); x.Text != "a,b\n1,2\n" {
		t.Fatalf("csv: %q", x.Text)
	}
	big := strings.Repeat("é", files.MaxTextLength) // 2 bytes each
	if x := extract(t, "big.txt", []byte(big)); !x.Truncated || len(x.Text) > files.MaxTextLength || !strings.HasSuffix(x.Text, "é") {
		t.Fatalf("truncation: %d %v", len(x.Text), x.Truncated)
	}
	for name, data := range map[string][]byte{
		"photo.png": {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0},
		"bad.pdf":   []byte("%PDF-1.4 garbage"),
		"bad.docx":  []byte("not a zip"),
		"bin.txt":   {0xff, 0xfe, 0x00},
	} {
		if _, err := files.ExtractText(files.File{Name: name, ContentType: files.ContentType(name, data)}, data); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestNamesAndTypes(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf": "report.pdf", `C:\Users\ada\notes.txt`: "notes.txt", "../../etc/passwd": "passwd", " a\x00b\x1f.md ": "ab.md",
		strings.Repeat("x", 300) + ".txt": strings.Repeat("x", 255),
	} {
		if got, err := files.CleanName(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "  ", "dir/", ".."} {
		if _, err := files.CleanName(bad); !errors.Is(err, files.ErrInvalid) {
			t.Fatalf("%q accepted", bad)
		}
	}
	for name, want := range map[string]string{"a.PDF": "application/pdf", "a.md": "text/markdown", "a.bin": "text/plain", "a.jpeg": "image/jpeg"} {
		if got := files.ContentType(name, []byte("hello")); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

type memRepo struct{ rows map[uuid.UUID][]byte }

func (m *memRepo) Create(_ context.Context, f files.File, data []byte) (files.File, error) {
	m.rows[f.ID] = data
	return f, nil
}
func (m *memRepo) Get(context.Context, uuid.UUID, uuid.UUID) (files.File, error) {
	return files.File{}, nil
}
func (m *memRepo) Content(context.Context, uuid.UUID, uuid.UUID) (files.File, []byte, error) {
	return files.File{}, nil, nil
}
func (m *memRepo) Locate(context.Context, uuid.UUID) (files.File, error) { return files.File{}, nil }
func (m *memRepo) List(context.Context, uuid.UUID) ([]files.File, error) { return nil, nil }
func (m *memRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error    { return nil }

func TestUploadValidation(t *testing.T) {
	svc := files.NewService(&memRepo{rows: map[uuid.UUID][]byte{}})
	f, err := svc.Upload(context.Background(), uuid.New(), uuid.New(), "notes.txt", []byte("hi"))
	if err != nil || f.Size != 2 || f.ContentType != "text/plain" || len(f.SHA256) != 64 {
		t.Fatalf("upload: %+v %v", f, err)
	}
	if _, err := svc.Upload(context.Background(), uuid.New(), uuid.New(), "e.txt", nil); !errors.Is(err, files.ErrInvalid) {
		t.Fatal("empty file")
	}
	if _, err := svc.Upload(context.Background(), uuid.New(), uuid.New(), "big.bin", make([]byte, files.MaxFileSize+1)); !errors.Is(err, files.ErrInvalid) {
		t.Fatal("too large")
	}
}
