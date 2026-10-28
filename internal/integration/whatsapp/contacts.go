package whatsapp

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// MatchContacts prefers exact IDs/names, then whole-name parts, then spelling
// similarity. One result means a clear match; multiple results require a human
// choice. Numbers and JIDs are never corrected to a nearby number or address.
func MatchContacts(contacts []Contact, query string) []Contact {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	for _, contact := range contacts {
		if contact.ID == query {
			return []Contact{contact}
		}
	}
	if strings.Contains(query, "@") {
		return nil
	}
	name := normalizeContactName(query)
	if name == "" {
		return nil
	}
	phone := strings.TrimLeft(name, " ")
	if strings.HasPrefix(query, "+") || strings.IndexFunc(phone, unicode.IsLetter) < 0 {
		digits := strings.Join(strings.Fields(phone), "")
		matches := []Contact{}
		for _, contact := range contacts {
			address, domain, jid := strings.Cut(contact.ID, "@")
			if len(digits) >= 7 && address == digits && (!jid || domain == "s.whatsapp.net") {
				matches = append(matches, contact)
			}
		}
		return matches
	}
	exact, partial := []Contact{}, []Contact{}
	for _, contact := range contacts {
		candidate := normalizeContactName(contact.Name)
		if candidate == name {
			exact = append(exact, contact)
		} else if strings.Contains(" "+candidate+" ", " "+name+" ") {
			partial = append(partial, contact)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(partial) > 0 {
		return partial
	}
	if size := len([]rune(strings.ReplaceAll(name, " ", ""))); size < 4 || size > 128 {
		return nil
	}
	type match struct {
		contact Contact
		score   float64
	}
	ranked := make([]match, 0, len(contacts))
	for _, contact := range contacts {
		candidate := normalizeContactName(contact.Name)
		if len([]rune(candidate)) <= 128 {
			ranked = append(ranked, match{contact, contactSimilarity(name, candidate)})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].contact.ID < ranked[j].contact.ID
	})
	if len(ranked) == 0 || ranked[0].score < .78 {
		return nil
	}
	matches := []Contact{ranked[0].contact}
	for _, candidate := range ranked[1:] {
		if ranked[0].score-candidate.score >= .08 {
			break
		}
		matches = append(matches, candidate.contact)
	}
	return matches
}

func normalizeContactName(value string) string {
	var out strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(value)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func contactSimilarity(query, name string) float64 {
	if name == "" {
		return 0
	}
	q, n := strings.Fields(query), strings.Fields(name)
	best := spellingSimilarity(strings.Join(q, ""), strings.Join(n, ""))
	sort.Strings(q)
	sort.Strings(n)
	best = max(best, spellingSimilarity(strings.Join(q, ""), strings.Join(n, "")))
	if len(q) > len(n) {
		return best
	}
	// A misspelled surname can match a full contact name, while a full requested
	// name must account for every token rather than matching only its surname.
	used := make([]bool, len(n))
	var score float64
	for _, token := range q {
		index, closest := -1, -1.0
		for i, candidate := range n {
			if value := spellingSimilarity(token, candidate); !used[i] && value > closest {
				index, closest = i, value
			}
		}
		if index < 0 {
			return best
		}
		used[index] = true
		score += closest
	}
	return max(best, score/float64(len(q)))
}

// Optimal-string-alignment distance also handles adjacent speech/typing swaps.
func spellingSimilarity(left, right string) float64 {
	a, b := []rune(left), []rune(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return 1 - float64(d[len(a)][len(b)])/float64(max(len(a), len(b)))
}
