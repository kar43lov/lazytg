package tg

import (
	"testing"

	"github.com/gotd/td/session"
)

func TestNew_RejectsMissingCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  ClientConfig
	}{
		{"zero APIID", ClientConfig{APIID: 0, APIHash: "h", SessionStore: &session.StorageMemory{}}},
		{"empty hash", ClientConfig{APIID: 1, APIHash: "", SessionStore: &session.StorageMemory{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatalf("New must fail when API credentials are missing")
			}
		})
	}
}

func TestNew_RejectsMissingSessionStore(t *testing.T) {
	t.Parallel()
	if _, err := New(ClientConfig{APIID: 1, APIHash: "h"}); err == nil {
		t.Fatalf("New must require a session store")
	}
}

func TestNew_AcceptsValidConfig(t *testing.T) {
	t.Parallel()
	c, err := New(ClientConfig{APIID: 1, APIHash: "h", SessionStore: &session.StorageMemory{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Raw() == nil {
		t.Fatalf("Raw() returned nil")
	}
}
