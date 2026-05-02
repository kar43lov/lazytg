package tg

import (
	"strings"
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

func TestCredentialsFromEnv(t *testing.T) {
	t.Setenv(EnvAPIID, "12345")
	t.Setenv(EnvAPIHash, "deadbeefcafebabe")

	id, hash, err := CredentialsFromEnv()
	if err != nil {
		t.Fatalf("CredentialsFromEnv: %v", err)
	}
	if id != 12345 || hash != "deadbeefcafebabe" {
		t.Fatalf("CredentialsFromEnv: want (12345, deadbeefcafebabe), got (%d, %q)", id, hash)
	}
}

func TestCredentialsFromEnv_MissingAPIID(t *testing.T) {
	t.Setenv(EnvAPIID, "")
	t.Setenv(EnvAPIHash, "h")
	_, _, err := CredentialsFromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvAPIID) {
		t.Fatalf("want error mentioning %s, got %v", EnvAPIID, err)
	}
}

func TestCredentialsFromEnv_BadAPIID(t *testing.T) {
	t.Setenv(EnvAPIID, "not-a-number")
	t.Setenv(EnvAPIHash, "h")
	_, _, err := CredentialsFromEnv()
	if err == nil {
		t.Fatalf("want error on non-numeric APIID")
	}
}

func TestCredentialsFromEnv_MissingHash(t *testing.T) {
	t.Setenv(EnvAPIID, "1")
	t.Setenv(EnvAPIHash, "")
	_, _, err := CredentialsFromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvAPIHash) {
		t.Fatalf("want error mentioning %s, got %v", EnvAPIHash, err)
	}
}
