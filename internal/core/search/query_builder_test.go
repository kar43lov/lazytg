package search_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/search"
)

// TestBuildSQL covers each Query field's contribution to the generated
// fragments. The table style mirrors parser_test.go so adding a new
// operator stays a one-line change in both places.
func TestBuildSQL(t *testing.T) {
	mustDate := func(s string) time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02", s, time.UTC)
		if err != nil {
			t.Fatalf("test fixture: parse %q: %v", s, err)
		}
		return v
	}

	cases := []struct {
		name         string
		q            search.Query
		wantSQL      string
		wantFTSMatch string
		wantArgs     []any
	}{
		{
			name:         "text only",
			q:            search.Query{Text: "hello"},
			wantSQL:      "",
			wantFTSMatch: `"hello"`,
			wantArgs:     nil,
		},
		{
			name:         "two text terms (FTS5 implicit AND)",
			q:            search.Query{Text: "hello world"},
			wantSQL:      "",
			wantFTSMatch: `"hello" "world"`,
			wantArgs:     nil,
		},
		{
			name:         "single phrase",
			q:            search.Query{Phrases: []string{"hello world"}},
			wantSQL:      "",
			wantFTSMatch: `"hello world"`,
			wantArgs:     nil,
		},
		{
			name:         "text + phrase",
			q:            search.Query{Text: "free", Phrases: []string{"glued together"}},
			wantSQL:      "",
			wantFTSMatch: `"free" "glued together"`,
			wantArgs:     nil,
		},
		{
			name:         "exclusion folded into NOT clause",
			q:            search.Query{Text: "важное", Excluded: []string{"спам"}},
			wantSQL:      "",
			wantFTSMatch: `("важное") NOT ("спам")`,
			wantArgs:     nil,
		},
		{
			name:         "two exclusions OR-combined inside NOT",
			q:            search.Query{Text: "важное", Excluded: []string{"спам", "реклама"}},
			wantSQL:      "",
			wantFTSMatch: `("важное") NOT ("спам" OR "реклама")`,
			wantArgs:     nil,
		},
		{
			name:         "from operator emits chats subquery",
			q:            search.Query{Text: "hi", From: []string{"alice"}},
			wantSQL:      " AND m.from_id IN (SELECT id FROM chats WHERE username IN (?))",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{"alice"},
		},
		{
			name:         "two from operators expand placeholders",
			q:            search.Query{Text: "hi", From: []string{"alice", "bob"}},
			wantSQL:      " AND m.from_id IN (SELECT id FROM chats WHERE username IN (?,?))",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{"alice", "bob"},
		},
		{
			name:         "in operator OR-joins username and title (args duplicated)",
			q:            search.Query{Text: "hi", InChats: []string{"general"}},
			wantSQL:      " AND m.chat_id IN (SELECT id FROM chats WHERE username IN (?) OR title IN (?))",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{"general", "general"},
		},
		{
			name:         "after only",
			q:            search.Query{Text: "hi", After: ptrTime(mustDate("2025-11-01"))},
			wantSQL:      " AND m.date >= ?",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{mustDate("2025-11-01").Unix()},
		},
		{
			name:         "before only",
			q:            search.Query{Text: "hi", Before: ptrTime(mustDate("2025-12-01"))},
			wantSQL:      " AND m.date < ?",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{mustDate("2025-12-01").Unix()},
		},
		{
			name: "before + after combine",
			q: search.Query{
				Text:   "hi",
				After:  ptrTime(mustDate("2025-11-01")),
				Before: ptrTime(mustDate("2025-12-01")),
			},
			wantSQL:      " AND m.date >= ? AND m.date < ?",
			wantFTSMatch: `"hi"`,
			wantArgs:     []any{mustDate("2025-11-01").Unix(), mustDate("2025-12-01").Unix()},
		},
		{
			name:         "has:file filters non-NULL media_kind (added in Task 6)",
			q:            search.Query{Text: "video", HasFile: true},
			wantSQL:      " AND m.media_kind IS NOT NULL",
			wantFTSMatch: `"video"`,
			wantArgs:     nil,
		},
		{
			// Regression: free-text tokens are quoted as FTS5 phrases so
			// FTS5 grammar tokens (AND/OR/NOT/NEAR, '*', '^', ':' etc.)
			// in user input become literal trigrams instead of triggering
			// the FTS5 query language.
			name:         "FTS5 grammar in free text is neutralised",
			q:            search.Query{Text: "name OR delete"},
			wantSQL:      "",
			wantFTSMatch: `"name" "OR" "delete"`,
			wantArgs:     nil,
		},
		{
			// Regression: phrase quoting now uses FTS5's doubled-quote
			// escape ("a""b") instead of Go's %q backslash form ("a\"b"),
			// which FTS5 cannot parse.
			name:         "phrase with embedded double quote uses FTS5 doubling",
			q:            search.Query{Phrases: []string{`hello "world"`}},
			wantSQL:      "",
			wantFTSMatch: `"hello ""world"""`,
			wantArgs:     nil,
		},
		{
			name: "kitchen sink — every operator together",
			q: search.Query{
				Text:     "слово",
				Phrases:  []string{"точная фраза"},
				Excluded: []string{"плохой"},
				From:     []string{"alice"},
				InChats:  []string{"general"},
				After:    ptrTime(mustDate("2025-11-01")),
				Before:   ptrTime(mustDate("2025-12-01")),
				HasFile:  true,
			},
			wantSQL: " AND m.from_id IN (SELECT id FROM chats WHERE username IN (?))" +
				" AND m.chat_id IN (SELECT id FROM chats WHERE username IN (?) OR title IN (?))" +
				" AND m.date >= ?" +
				" AND m.date < ?" +
				" AND m.media_kind IS NOT NULL",
			wantFTSMatch: `("слово" "точная фраза") NOT ("плохой")`,
			wantArgs: []any{
				"alice",
				"general", "general",
				mustDate("2025-11-01").Unix(),
				mustDate("2025-12-01").Unix(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSQL, gotMatch, gotArgs, err := search.BuildSQL(c.q)
			if err != nil {
				t.Fatalf("BuildSQL returned error: %v", err)
			}
			if gotSQL != c.wantSQL {
				t.Errorf("sqlText\n got = %q\nwant = %q", gotSQL, c.wantSQL)
			}
			if gotMatch != c.wantFTSMatch {
				t.Errorf("ftsMatch\n got = %q\nwant = %q", gotMatch, c.wantFTSMatch)
			}
			if !reflect.DeepEqual(gotArgs, c.wantArgs) {
				t.Errorf("args\n got = %#v\nwant = %#v", gotArgs, c.wantArgs)
			}
		})
	}
}

// TestBuildSQL_Errors guards the defensive empty-MATCH case — Parse
// rejects this input but BuildSQL must not silently produce a query
// that returns every row when a future caller hand-builds a Query.
func TestBuildSQL_Errors(t *testing.T) {
	_, _, _, err := search.BuildSQL(search.Query{Excluded: []string{"spam"}})
	if err == nil {
		t.Fatalf("BuildSQL with only Excluded: want error, got nil")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
