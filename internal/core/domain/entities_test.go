package domain

import (
	"reflect"
	"testing"
)

func TestEntities_RoundTrip(t *testing.T) {
	t.Parallel()

	in := []Entity{
		{Kind: EntityBold, Offset: 0, Length: 5},
		{Kind: EntityTextURL, Offset: 6, Length: 4, URL: "https://example.com/x"},
		{Kind: EntityPre, Offset: 11, Length: 20, Language: "go"},
		{Kind: EntityMentionName, Offset: 32, Length: 3, UserID: 42},
		{Kind: EntityCustomEmoji, Offset: 36, Length: 1, DocumentID: 7},
	}
	got := DecodeEntities(EncodeEntities(in))
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip changed the spans:\n got %+v\nwant %+v", got, in)
	}
}

func TestEntities_NoneIsTheEmptyString(t *testing.T) {
	t.Parallel()

	if s := EncodeEntities(nil); s != "" {
		t.Fatalf("nil encodes as %q, want the column default", s)
	}
	if s := EncodeEntities([]Entity{{Kind: EntityBold, Offset: 0, Length: 0}}); s != "" {
		t.Fatalf("an empty span encodes as %q, want nothing", s)
	}
	if got := DecodeEntities(""); got != nil {
		t.Fatalf("empty column decodes to %v, want nil", got)
	}
	if got := DecodeEntities("not json"); got != nil {
		t.Fatalf("garbage decodes to %v, want nil rather than an error", got)
	}
}

func TestSortEntities_OuterSpanFirst(t *testing.T) {
	t.Parallel()

	es := []Entity{
		{Kind: EntityItalic, Offset: 2, Length: 3},
		{Kind: EntityBold, Offset: 2, Length: 8},
		{Kind: EntityCode, Offset: 0, Length: 1},
	}
	SortEntities(es)
	want := []EntityKind{EntityCode, EntityBold, EntityItalic}
	for i, k := range want {
		if es[i].Kind != k {
			t.Fatalf("position %d is %s, want %s (order %v)", i, es[i].Kind, k, es)
		}
	}
}
