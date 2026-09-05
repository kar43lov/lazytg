package emoji

import (
	"strings"
	"testing"
)

// The ranking is the feature. A completion that offers 🚒 fire engine before
// 🔥 for "fire" is one people stop using after two tries.
func TestSearch_PutsTheObviousAnswerFirst(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"fire":   "🔥",
		"rocket": "🚀",
		"joy":    "😂",
		"think":  "🤔",
		"+1":     "👍",
		"heart":  "❤",
		"tada":   "🎉",
		"eyes":   "👀",
	}
	for q, char := range want {
		hits := Search(q)
		if len(hits) == 0 {
			t.Errorf("Search(%q) found nothing", q)
			continue
		}
		if hits[0].Char != char {
			t.Errorf("Search(%q)[0] = %q, want %q", q, hits[0].Char, char)
		}
	}
}

// An alias may name a character newer than the Unicode data the table was
// generated from. Dropping it for bookkeeping reasons would mean the user
// typed a name this package knows and got nothing.
func TestSearch_KeepsAnAliasTheTableDoesNotCarry(t *testing.T) {
	t.Parallel()

	var missing string
	for code, char := range aliases {
		found := false
		for _, e := range entries {
			if e.Char == char {
				found = true
				break
			}
		}
		if !found {
			missing = code
			break
		}
	}
	if missing == "" {
		t.Skip("every alias is in the generated table on this build")
	}
	hits := Search(missing)
	if len(hits) == 0 || hits[0].Char != aliases[missing] {
		t.Fatalf("Search(%q) lost the alias; got %v", missing, hits)
	}
}

func TestSearch_EmptyQueryReturnsEverything(t *testing.T) {
	t.Parallel()

	if got, want := len(Search("")), len(entries); got != want {
		t.Fatalf("Search(\"\") returned %d entries, want %d", got, want)
	}
	if got := len(Search("::")); got != len(entries) {
		t.Fatalf("a query of nothing but colons returned %d entries", got)
	}
}

func TestSearch_FindsNothingForNonsense(t *testing.T) {
	t.Parallel()

	if hits := Search("zzzqqq"); len(hits) != 0 {
		t.Fatalf("Search(nonsense) returned %d hits: %v", len(hits), hits[0])
	}
}

// The picker opens on the first category, and people come to it for faces.
func TestCategories_StartWithSmileys(t *testing.T) {
	t.Parallel()

	cats := Categories()
	if len(cats) == 0 {
		t.Fatal("no categories")
	}
	if cats[0] != "Smileys" {
		t.Fatalf("first category is %q, want Smileys", cats[0])
	}
	for _, c := range cats {
		if len(InCategory(c)) == 0 {
			t.Errorf("category %q is listed but empty", c)
		}
	}
}

// The table is generated, so this guards the generator rather than the data:
// a duplicate shortcode means two different emoji answer to one name and the
// completion silently picks one.
func TestTable_ShortcodesAreUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		if prev, ok := seen[e.Code]; ok {
			t.Fatalf("shortcode %q is used by both %q and %q", e.Code, prev, e.Char)
		}
		seen[e.Code] = e.Char
	}
}

func TestTable_EveryEntryIsUsable(t *testing.T) {
	t.Parallel()

	for _, e := range entries {
		if e.Char == "" || e.Code == "" || e.Name == "" || e.Category == "" {
			t.Fatalf("incomplete entry: %+v", e)
		}
		if strings.ContainsAny(e.Code, " :") {
			t.Fatalf("shortcode %q cannot be typed after a colon", e.Code)
		}
	}
	if len(entries) < 500 {
		t.Fatalf("only %d emoji in the table — the generator lost most of them", len(entries))
	}
}

// All hands out a copy, or a caller sorting the result would reorder the
// package's own table for everybody else.
func TestAll_DoesNotHandOutTheTable(t *testing.T) {
	t.Parallel()

	got := All()
	if len(got) == 0 {
		t.Fatal("All returned nothing")
	}
	first := entries[0]
	got[0] = Entry{Char: "x"}
	if entries[0] != first {
		t.Fatal("All handed out the package's own slice")
	}
}
