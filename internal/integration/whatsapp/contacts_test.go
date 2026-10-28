package whatsapp

import (
	"reflect"
	"sort"
	"testing"
)

func TestContactNamesResolveSpellingWithoutGuessingAmbiguousPeople(t *testing.T) {
	contacts := []Contact{
		{ID: "ajinkya", Name: "Ajinkya Phatak"}, {ID: "ritwik", Name: "Ritwik Sundar"},
		{ID: "other", Name: "Other Phatak"}, {ID: "jose", Name: "José Silva"},
		{ID: "ann", Name: "Ann Carey"}, {ID: "anna", Name: "Anna"},
		{ID: "zoe", Name: "Zoe Adams"},
		{ID: "919876543210@s.whatsapp.net", Name: "Phone contact"},
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"ritwik", []string{"ritwik"}},
		{"Ajinkya Fatak", []string{"ajinkya"}},
		{"Ritwick Sunder", []string{"ritwik"}},
		{"Phatak Ajinkya", []string{"ajinkya"}},
		{" AJINKYA.PHATAK ", []string{"ajinkya"}},
		{"Jose Silva", []string{"jose"}},
		{"ZoeAdmas", []string{"zoe"}},
		{"Phatak", []string{"ajinkya", "other"}},
		{"Phatac", []string{"ajinkya", "other"}},
		{"Ann", []string{"ann"}},
		{"An", nil},
		{"Jessica Carter", nil},
		{"", nil},
		{"+91 98765-43210", []string{"919876543210@s.whatsapp.net"}},
		{"+91 98765-43211", nil},
		{"919876543211@s.whatsapp.net", nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var got []string
			for _, contact := range MatchContacts(contacts, tc.query) {
				got = append(got, contact.ID)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("query %q got %v; want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestExactNameWinsAndFuzzyTiesStayAmbiguous(t *testing.T) {
	exact := MatchContacts([]Contact{{ID: "one", Name: "Ankit"}, {ID: "two", Name: "Ankita"}}, "Ankit")
	if len(exact) != 1 || exact[0].ID != "one" {
		t.Fatal("an exact contact name lost to a partial match")
	}
	for _, contacts := range [][]Contact{
		{{ID: "one", Name: "Ankit"}, {ID: "two", Name: "Ankita"}},
		{{ID: "two", Name: "Ankita"}, {ID: "one", Name: "Ankit"}},
	} {
		if matches := MatchContacts(contacts, "Ankith"); len(matches) != 2 {
			t.Fatalf("a fuzzy tie was auto-selected: %+v", matches)
		}
	}
}
