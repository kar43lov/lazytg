package domain

import (
	"testing"
	"time"
)

func TestForward_RoundTrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	f := &Forward{From: "News", FromID: 500, Date: at}
	got := DecodeForward(EncodeForward(f))
	if got == nil || got.From != "News" || got.FromID != 500 || !got.Date.Equal(at) {
		t.Fatalf("round trip = %+v", got)
	}
	hidden := DecodeForward(EncodeForward(&Forward{From: "Somebody"}))
	if hidden == nil || hidden.From != "Somebody" || hidden.FromID != 0 || !hidden.Date.IsZero() {
		t.Fatalf("hidden sender = %+v", hidden)
	}
	if EncodeForward(nil) != "" || DecodeForward("") != nil || DecodeForward("junk") != nil {
		t.Fatal("no origin must be the empty string and read back as nil")
	}
}
