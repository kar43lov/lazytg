package search_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/search"
)

// TestParse covers the documented grammar in a single table-driven test so
// adding a new operator stays a one-line change.
func TestParse(t *testing.T) {
	mustDate := func(s string) *time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02", s, time.UTC)
		if err != nil {
			t.Fatalf("test fixture: parse %q: %v", s, err)
		}
		return &v
	}

	cases := []struct {
		name string
		in   string
		want search.Query
	}{
		{
			name: "single bare term",
			in:   "hello",
			want: search.Query{Text: "hello"},
		},
		{
			name: "two bare terms join with space (FTS5 implicit AND)",
			in:   "hello world",
			want: search.Query{Text: "hello world"},
		},
		{
			name: "phrase token preserved as one Phrases entry",
			in:   `"hello world"`,
			want: search.Query{Phrases: []string{"hello world"}},
		},
		{
			name: "from operator with @ prefix strips it",
			in:   "from:@alice hello",
			want: search.Query{From: []string{"alice"}, Text: "hello"},
		},
		{
			name: "two from operators accumulate",
			in:   "from:@alice from:@bob hello",
			want: search.Query{From: []string{"alice", "bob"}, Text: "hello"},
		},
		{
			name: "from operator without @ prefix still works",
			in:   "from:bob hello",
			want: search.Query{From: []string{"bob"}, Text: "hello"},
		},
		{
			name: "in operator with # prefix strips it",
			in:   "in:#general hello",
			want: search.Query{InChats: []string{"general"}, Text: "hello"},
		},
		{
			name: "before and after combine with text",
			in:   "before:2025-12-01 after:2025-11-01 спам",
			want: search.Query{
				Before: mustDate("2025-12-01"),
				After:  mustDate("2025-11-01"),
				Text:   "спам",
			},
		},
		{
			name: "has:file with text",
			in:   "has:file видео",
			want: search.Query{HasFile: true, Text: "видео"},
		},
		{
			name: "exclusion with text",
			in:   "-spam важное",
			want: search.Query{Excluded: []string{"spam"}, Text: "важное"},
		},
		{
			name: "phrase + free text + exclusion",
			in:   `"точная фраза" свободный -ненужный`,
			want: search.Query{
				Phrases:  []string{"точная фраза"},
				Excluded: []string{"ненужный"},
				Text:     "свободный",
			},
		},
		{
			name: "phrase with escaped quote",
			in:   `"hello \"world\""`,
			want: search.Query{Phrases: []string{`hello "world"`}},
		},
		{
			name: "unknown operator falls through to text (URL-style colon)",
			in:   "RFC:7230 spec",
			want: search.Query{Text: "RFC:7230 spec"},
		},
		{
			// Regression: applyOperator's default branch used to write
			// to q.Text directly, but Parse overwrote q.Text on its way
			// out, silently dropping the lowercase-keyed unknown
			// operator. Now the token is preserved as plain text.
			name: "unknown lowercase operator preserved as text",
			in:   "foo:bar baz",
			want: search.Query{Text: "foo:bar baz"},
		},
		{
			name: "bare dash is not an exclusion",
			in:   "- minus sign",
			want: search.Query{Text: "- minus sign"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := search.Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", c.in, err)
			}
			// Raw is set unconditionally — copy it into want so the
			// table stays focused on the interesting fields.
			c.want.Raw = c.in
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse(%q)\n got = %+v\nwant = %+v", c.in, got, c.want)
			}
		})
	}
}

// TestParse_Errors exercises the negative cases. Each row asserts the
// error message contains a recognisable hint so the UI can surface
// something useful without round-tripping through %v formatting.
func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantContain string
	}{
		{"empty input", "", "empty query"},
		{"whitespace only", "   ", "empty query"},
		{"unclosed quote", `"foo`, "unclosed quote"},
		{"invalid before date", "before:invalid", "before:invalid"},
		{"unsupported has value", "has:everything", "has:everything"},
		{"only exclusion no positive term", "-spam", "no positive search terms"},
		{"from operator with empty username", "from:@ hello", "username required"},
		{"in operator with empty chat name", "in:# hello", "chat name required"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := search.Parse(c.in)
			if err == nil {
				t.Fatalf("Parse(%q): want error, got nil", c.in)
			}
			if !strings.Contains(err.Error(), c.wantContain) {
				t.Errorf("Parse(%q): err = %q, want substring %q", c.in, err.Error(), c.wantContain)
			}
		})
	}
}
