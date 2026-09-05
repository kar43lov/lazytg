package domain

import (
	"encoding/json"
	"time"
)

// Forward says where a forwarded message came from: the name of the
// person or channel that wrote it, their id when they did not hide it,
// and when they wrote it. A message forwarded from someone who hides
// their account carries the name alone, which is what every client
// shows in that case.
type Forward struct {
	From   string
	FromID int64
	Date   time.Time
}

type storedForward struct {
	N string `json:"n,omitempty"`
	I int64  `json:"i,omitempty"`
	D int64  `json:"d,omitempty"`
}

// EncodeForward renders the origin for storage; no origin is the empty
// string, the column default.
func EncodeForward(f *Forward) string {
	if f == nil {
		return ""
	}
	var d int64
	if !f.Date.IsZero() {
		d = f.Date.Unix()
	}
	raw, err := json.Marshal(storedForward{N: f.From, I: f.FromID, D: d})
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeForward is the inverse; malformed input reads as no origin.
func DecodeForward(s string) *Forward {
	if s == "" {
		return nil
	}
	var stored storedForward
	if err := json.Unmarshal([]byte(s), &stored); err != nil {
		return nil
	}
	f := &Forward{From: stored.N, FromID: stored.I}
	if stored.D != 0 {
		f.Date = time.Unix(stored.D, 0).UTC()
	}
	return f
}
