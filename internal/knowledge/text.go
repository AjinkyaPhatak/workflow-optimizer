package knowledge

import (
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunking: passages of about ChunkSize characters, overlapping by
// ChunkOverlap so a sentence cut at a boundary is still found whole.
const (
	ChunkSize    = 1200
	ChunkOverlap = 200
	MaxChunks    = 4000
)

// Split cuts text into overlapping passages, preferring paragraph, then
// line, then sentence, then word boundaries. Whitespace-only text yields
// none.
func Split(text string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	var out []string
	for text != "" && len(out) < MaxChunks {
		if utf8.RuneCountInString(text) <= ChunkSize {
			out = append(out, text)
			break
		}
		r := []rune(text)
		window := string(r[:ChunkSize])
		cut := len(window)
		for _, sep := range []string{"\n\n", "\n", ". ", "? ", "! ", "; ", " "} {
			if i := strings.LastIndex(window, sep); i >= len(window)/2 {
				cut = i + len(sep)
				break
			}
		}
		chunk := strings.TrimSpace(window[:cut])
		if chunk != "" {
			out = append(out, chunk)
		}
		// Step back by the overlap, to a word start.
		next := cut - overlapBytes(window[:cut])
		if next <= 0 {
			next = cut
		} else if i := strings.IndexFunc(window[next:cut], unicode.IsSpace); i >= 0 {
			next += i // start the overlap at a word boundary
		} else {
			next = cut
		}
		text = strings.TrimSpace(text[next:])
	}
	return out
}

// overlapBytes is the byte length of the last ChunkOverlap characters.
func overlapBytes(s string) int {
	n, i := 0, len(s)
	for i > 0 && n < ChunkOverlap {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
		n++
	}
	return len(s) - i
}

// Normalize scales v to unit length (a zero vector stays zero).
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if sum == 0 {
		return out
	}
	n := float32(1 / math.Sqrt(sum))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

// Dot is the dot product (the cosine similarity of unit vectors).
func Dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Encode stores a vector as little-endian float32 bytes.
func Encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// Decode reads Encode's bytes.
func Decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

type scored struct {
	id    int64
	score float64
}

// Top returns the k best-scoring vectors at or above minScore, best first.
func Top(query []float32, candidates []ScoredID, k int, minScore float64) []scored {
	all := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if s := Dot(query, c.Vector); s >= minScore {
			all = append(all, scored{c.ID, s})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].id < all[j].id
	})
	if len(all) > k {
		all = all[:k]
	}
	return all
}
