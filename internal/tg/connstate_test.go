package tg

import (
	"testing"
	"time"

	"github.com/gotd/td/session"
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

// newTestClient builds a Client without touching the network. gotd's
// constructor does no I/O, so this exercises the real wiring.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(ClientConfig{APIID: 1, APIHash: "hash", SessionStore: &session.StorageMemory{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestClient_RotatesTheGotdClientBetweenSessions is the reason reconnect can
// work at all. gotd's Client is single-use: Run cancels the client context on
// the way out, so the next Run returns "client already closed" without
// sending a packet. A supervisor that restarts sessions on one gotd client
// would fail every time, forever, and report it as a network problem.
func TestClient_RotatesTheGotdClientBetweenSessions(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)

	built := c.current()
	if first := c.rotate(); first != built {
		t.Fatal("the first session got a different client than New built — a login holding Raw() would be talking to a discarded one")
	}
	second := c.rotate()
	if second == built {
		t.Fatal("the second session reused the spent client — every reconnect would fail with \"client already closed\"")
	}
	if c.current() != second {
		t.Fatal("current() did not follow the rotation")
	}
}

// TestClient_APISurvivesRotation pins the other half. Services capture
// Client.API() at attach time and the UI panes capture those services at
// construction, so the object handed out has to be the same one forever —
// swapping what it resolves to is the only safe way to reconnect underneath
// a running TUI.
func TestClient_APISurvivesRotation(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)

	api := c.API()
	c.rotate()
	c.rotate()
	if c.API() != api {
		t.Fatal("API() returned a new object after a reconnect — every service wired at attach would still hold the old one")
	}
}
