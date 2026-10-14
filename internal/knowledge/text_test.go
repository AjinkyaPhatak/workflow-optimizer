package knowledge_test

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"workflow-optimizer/internal/knowledge"
)

func TestSplit(t *testing.T) {
	if got := knowledge.Split("  \n "); len(got) != 0 {
		t.Fatalf("blank: %q", got)
	}
	if got := knowledge.Split("Short note."); len(got) != 1 || got[0] != "Short note." {
		t.Fatalf("short: %q", got)
	}
	// Paragraphs of numbered sentences: every sentence survives whole in
	// some chunk, chunks stay within the size, and neighbours overlap.
	var b strings.Builder
	for i := 0; i < 120; i++ {
		b.WriteString("Sentence number " + strings.Repeat("x", i%7) + " " + string(rune('A'+i%26)) + " ends here. ")
		if i%10 == 9 {
			b.WriteString("\n\n")
		}
	}
	text := b.String()
	chunks := knowledge.Split(text)
	if len(chunks) < 3 {
		t.Fatalf("chunks: %d", len(chunks))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > knowledge.ChunkSize {
			t.Fatalf("chunk %d has %d characters", i, utf8.RuneCountInString(c))
		}
		if i > 0 {
			tail := chunks[i-1][len(chunks[i-1])-40:]
			if !strings.Contains(c, strings.TrimSpace(tail[strings.Index(tail, " ")+1:])) {
				t.Fatalf("chunk %d does not overlap the previous one", i)
			}
		}
	}
	// Multi-byte text is never cut inside a character.
	hindi := strings.Repeat("नमस्ते दुनिया यह एक परीक्षण है। ", 200)
	for i, c := range knowledge.Split(hindi) {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
	}
	// A single unbroken word longer than a chunk still progresses.
	if got := knowledge.Split(strings.Repeat("a", 3000)); len(got) < 3 {
		t.Fatalf("unbroken text: %d chunks", len(got))
	}
}

func TestVectors(t *testing.T) {
	v := knowledge.Normalize([]float32{3, 4})
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("normalize: %v", v)
	}
	if z := knowledge.Normalize([]float32{0, 0}); z[0] != 0 || z[1] != 0 {
		t.Fatal("zero vector")
	}
	if got := knowledge.Decode(knowledge.Encode([]float32{1.5, -2, 0.25})); got[0] != 1.5 || got[1] != -2 || got[2] != 0.25 {
		t.Fatalf("round trip: %v", got)
	}
	if knowledge.Dot([]float32{1, 0}, []float32{1}) != 0 {
		t.Fatal("mismatched lengths score 0")
	}
	q := knowledge.Normalize([]float32{1, 0})
	cands := []knowledge.ScoredID{
		{ID: 1, Vector: knowledge.Normalize([]float32{0, 1})},
		{ID: 2, Vector: knowledge.Normalize([]float32{1, 1})},
		{ID: 3, Vector: knowledge.Normalize([]float32{1, 0.1})},
	}
	top := knowledge.Top(q, cands, 2, 0.5)
	if len(top) != 2 {
		t.Fatalf("top: %v", top)
	}
	if got := knowledge.Top(q, cands, 5, 0.9); len(got) != 1 {
		t.Fatalf("min score: %v", got)
	}
}

func TestDocumentIDIsStable(t *testing.T) {
	a, b := knowledge.DocumentID([16]byte{1}, "exec-1/node"), knowledge.DocumentID([16]byte{1}, "exec-1/node")
	if a != b || a == knowledge.DocumentID([16]byte{2}, "exec-1/node") || a == knowledge.DocumentID([16]byte{1}, "exec-2/node") {
		t.Fatal("document IDs derive from the base and the key")
	}
}

func TestContext(t *testing.T) {
	got := knowledge.Context([]knowledge.Result{{DocumentTitle: "Policy", Content: "Refunds take 5 days."}, {DocumentTitle: "FAQ", Content: "Email us."}})
	if got != "[1] Policy\nRefunds take 5 days.\n\n[2] FAQ\nEmail us." {
		t.Fatalf("context: %q", got)
	}
}
