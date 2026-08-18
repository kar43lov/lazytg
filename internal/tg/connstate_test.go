package tg

import (
	"testing"
	"time"

	"github.com/gotd/td/telegram"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// TestConnectionStateName_MapsEveryGotdState pins the translation from gotd's
// enum to the strings the status bar renders. An unknown enum value maps to
// the empty string rather than to a word: a gotd upgrade that adds a state
// would otherwise put "unknown" in front of the user, where the honest answer
// is to keep showing the last state we understood.
func TestConnectionStateName_MapsEveryGotdState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state telegram.ConnectionState
		want  string
	}{
		{"ready is online", telegram.ConnectionStateReady, coresync.ConnectionStateOnline},
		{"connecting", telegram.ConnectionStateConnecting, coresync.ConnectionStateConnecting},
		{"disconnected is offline", telegram.ConnectionStateDisconnected, coresync.ConnectionStateOffline},
		{"unknown yields nothing", telegram.ConnectionState(42), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := connectionStateName(tt.state); got != tt.want {
				t.Fatalf("connectionStateName(%v) = %q want %q", tt.state, got, tt.want)
			}
		})
	}
}

// TestOnConnectionState_KeepsTheLatestValue covers the reason the feed is a
// one-slot channel rather than a queue. gotd calls the callback synchronously
// from its connection lifecycle and forbids blocking there, so a reader that
// fell behind must not stall the transport — and once it catches up, what it
// needs is where the connection is now, not a replay of the flapping it
// missed.
func TestOnConnectionState_KeepsTheLatestValue(t *testing.T) {
	t.Parallel()
	c := &Client{states: make(chan string, 1)}

	c.onConnectionState(telegram.ConnectionStateConnecting)
	c.onConnectionState(telegram.ConnectionStateDisconnected)
	c.onConnectionState(telegram.ConnectionStateReady)

	select {
	case got := <-c.ConnectionStates():
		if got != coresync.ConnectionStateOnline {
			t.Fatalf("state = %q want %q", got, coresync.ConnectionStateOnline)
		}
	default:
		t.Fatal("no state available — the callback dropped every value")
	}

	select {
	case got := <-c.ConnectionStates():
		t.Fatalf("second read yielded %q — the feed is queueing, not replacing", got)
	default:
	}
}

// TestOnConnectionState_NeverBlocks is the property that matters most: the
// callback runs on gotd's connection path, so a full buffer with no reader
// must not turn a status update into a stalled handshake.
func TestOnConnectionState_NeverBlocks(t *testing.T) {
	t.Parallel()
	c := &Client{states: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			c.onConnectionState(telegram.ConnectionStateConnecting)
			c.onConnectionState(telegram.ConnectionStateReady)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback blocked with a full buffer and no reader")
	}
}
