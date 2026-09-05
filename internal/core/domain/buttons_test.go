package domain

import (
	"reflect"
	"testing"
)

func TestButtons_RoundTrip(t *testing.T) {
	t.Parallel()

	rows := [][]Button{
		{{Text: "Yes", Kind: ButtonCallback, Data: []byte{0, 1, 255}}, {Text: "Docs", Kind: ButtonURL, URL: "https://example.com"}},
		{{Text: "/start", Kind: ButtonText}},
		{{Text: "Copy", Kind: ButtonCopy, URL: "secret"}, {Text: "Pay", Kind: ButtonOther}},
	}
	got := DecodeButtons(EncodeButtons(rows))
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, rows)
	}
	if EncodeButtons(nil) != "" || EncodeButtons([][]Button{{}}) != "" {
		t.Fatal("no keyboard must encode as the empty string")
	}
	if DecodeButtons("") != nil || DecodeButtons("not json") != nil || DecodeButtons("[[]]") != nil {
		t.Fatal("nothing, garbage and an empty keyboard all read as no keyboard")
	}
}
