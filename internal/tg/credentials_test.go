package tg

import (
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tgerr"
)

// withEmbedded swaps the build-time credentials for the duration of a test.
// The variables are package-level (they have to be, -ldflags -X can only
// write to those), so tests that touch them cannot run in parallel with each
// other — none of the tests below call t.Parallel.
func withEmbedded(t *testing.T, id, hash string) {
	t.Helper()
	prevID, prevHash := embeddedAPIID, embeddedAPIHash
	embeddedAPIID, embeddedAPIHash = id, hash
	t.Cleanup(func() { embeddedAPIID, embeddedAPIHash = prevID, prevHash })
}

// clearEnv removes both runtime vars so a resolution test is not affected by
// the developer's own shell.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvAPIID, "")
	t.Setenv(EnvAPIHash, "")
}

func TestResolveCredentials_Precedence(t *testing.T) {
	tests := []struct {
		name               string
		flagID, flagHash   string
		envID, envHash     string
		embedID, embedHash string
		wantID             int
		wantHash           string
		wantSrc            CredentialsSource
	}{
		{
			name:   "flags win over env and embedded",
			flagID: "111", flagHash: "flaghash",
			envID: "222", envHash: "envhash",
			embedID: "333", embedHash: "embedhash",
			wantID: 111, wantHash: "flaghash", wantSrc: SourceFlags,
		},
		{
			name:  "env wins over embedded",
			envID: "222", envHash: "envhash",
			embedID: "333", embedHash: "embedhash",
			wantID: 222, wantHash: "envhash", wantSrc: SourceEnv,
		},
		{
			name:    "embedded is the fallback",
			embedID: "333", embedHash: "embedhash",
			wantID: 333, wantHash: "embedhash", wantSrc: SourceEmbedded,
		},
		{
			name: "whitespace around injected values is tolerated",
			// A secret pasted into the CI settings UI often carries a
			// trailing newline; that must not turn into a parse failure
			// discovered only at release time.
			embedID: " 333\n", embedHash: " embedhash\n",
			wantID: 333, wantHash: "embedhash", wantSrc: SourceEmbedded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIID, tc.envID)
			t.Setenv(EnvAPIHash, tc.envHash)
			withEmbedded(t, tc.embedID, tc.embedHash)

			id, hash, src, err := ResolveCredentials(tc.flagID, tc.flagHash)
			if err != nil {
				t.Fatalf("ResolveCredentials: %v", err)
			}
			if id != tc.wantID || hash != tc.wantHash || src != tc.wantSrc {
				t.Fatalf("got (%d, %q, %s), want (%d, %q, %s)",
					id, hash, src, tc.wantID, tc.wantHash, tc.wantSrc)
			}
		})
	}
}

// A half-filled layer must fail loudly instead of falling through to the next
// one. Silently using the embedded key when the user thinks they configured
// their own is the failure mode that shows up weeks later as a ban on
// credentials they never chose.
func TestResolveCredentials_PartialLayerDoesNotFallThrough(t *testing.T) {
	tests := []struct {
		name             string
		flagID, flagHash string
		envID, envHash   string
		wantMentions     string
	}{
		{name: "flag id without hash", flagID: "111", wantMentions: "--api-hash"},
		{name: "flag hash without id", flagHash: "h", wantMentions: "--api-id"},
		{name: "env id without hash", envID: "222", wantMentions: EnvAPIHash},
		{name: "env hash without id", envHash: "h", wantMentions: EnvAPIID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIID, tc.envID)
			t.Setenv(EnvAPIHash, tc.envHash)
			withEmbedded(t, "999", "embedhash")

			_, _, src, err := ResolveCredentials(tc.flagID, tc.flagHash)
			if err == nil {
				t.Fatalf("want error, got success with source %s", src)
			}
			if !strings.Contains(err.Error(), tc.wantMentions) {
				t.Fatalf("error %q must name %s", err, tc.wantMentions)
			}
			if src != SourceNone {
				t.Fatalf("failed resolution must report source %q, got %q", SourceNone, src)
			}
		})
	}
}

func TestResolveCredentials_RejectsBadAPIID(t *testing.T) {
	tests := []struct {
		name  string
		envID string
	}{
		{"not a number", "not-a-number"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIID, tc.envID)
			t.Setenv(EnvAPIHash, "h")
			withEmbedded(t, "", "")

			if _, _, _, err := ResolveCredentials("", ""); err == nil {
				t.Fatalf("want error for api_id %q", tc.envID)
			}
		})
	}
}

// Swapping the two variables is a common paste error, and the resulting
// message goes to stderr through cobra — past the slog redaction handler.
// It must therefore never contain the offending value.
func TestResolveCredentials_ErrorDoesNotEchoSwappedSecret(t *testing.T) {
	const hashInIDSlot = "0123456789abcdef0123456789abcdef"
	t.Setenv(EnvAPIID, hashInIDSlot)
	t.Setenv(EnvAPIHash, "1234567")
	withEmbedded(t, "", "")

	_, _, _, err := ResolveCredentials("", "")
	if err == nil {
		t.Fatalf("want error when api_id holds a hash")
	}
	if strings.Contains(err.Error(), hashInIDSlot) {
		t.Fatalf("error must not echo the value, got: %s", err)
	}
	if !strings.Contains(err.Error(), EnvAPIHash) {
		t.Fatalf("error should hint at the swap by naming %s, got: %s", EnvAPIHash, err)
	}
}

// A source build has no embedded credentials, so the very first thing the
// user sees must be the my.telegram.org instruction.
func TestResolveCredentials_NoLayerYieldsActionableError(t *testing.T) {
	clearEnv(t)
	withEmbedded(t, "", "")

	_, _, src, err := ResolveCredentials("", "")
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("want ErrNoCredentials, got %v", err)
	}
	if src != SourceNone {
		t.Fatalf("want source %q, got %q", SourceNone, src)
	}
	for _, want := range []string{"my.telegram.org", EnvAPIID, EnvAPIHash} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ErrNoCredentials must mention %q, got: %s", want, err)
		}
	}
}

// A release built with only one of the two secrets configured is a build bug.
// The message must say so, otherwise the user audits their own environment
// for a problem that is not theirs.
func TestResolveCredentials_PartialEmbeddedIsReportedAsBuildBug(t *testing.T) {
	clearEnv(t)
	withEmbedded(t, "333", "")

	_, _, _, err := ResolveCredentials("", "")
	if err == nil {
		t.Fatalf("want error for half-injected build credentials")
	}
	if !strings.Contains(err.Error(), "report this") {
		t.Fatalf("error must flag itself as a build bug, got: %s", err)
	}
}

func TestHasEmbeddedCredentials(t *testing.T) {
	tests := []struct {
		name     string
		id, hash string
		want     bool
	}{
		{"both present", "333", "embedhash", true},
		{"neither", "", "", false},
		{"id only", "333", "", false},
		{"hash only", "", "embedhash", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withEmbedded(t, tc.id, tc.hash)
			if got := HasEmbeddedCredentials(); got != tc.want {
				t.Fatalf("HasEmbeddedCredentials() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExplainCredentialError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		wantMentions []string
		wantSame     bool
	}{
		{
			name:         "published flood gets remediation",
			err:          tgerr.New(400, "API_ID_PUBLISHED_FLOOD"),
			wantMentions: []string{"my.telegram.org", EnvAPIID, "lazytg version"},
		},
		{
			name:         "invalid id gets remediation",
			err:          tgerr.New(400, "API_ID_INVALID"),
			wantMentions: []string{"my.telegram.org", "lazytg version"},
		},
		{
			name:     "unrelated error passes through untouched",
			err:      tgerr.New(420, "FLOOD_WAIT_3"),
			wantSame: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExplainCredentialError(tc.err)
			if tc.wantSame {
				if got != tc.err {
					t.Fatalf("unrelated error must pass through unchanged, got %v", got)
				}
				return
			}
			// The original error must stay in the chain so callers that
			// match on the RPC code (retry policies) keep working.
			if !errors.Is(got, tc.err) {
				t.Fatalf("original error must remain wrapped, got %v", got)
			}
			for _, want := range tc.wantMentions {
				if !strings.Contains(got.Error(), want) {
					t.Fatalf("explanation must mention %q, got: %s", want, got)
				}
			}
		})
	}
}

func TestExplainCredentialError_Nil(t *testing.T) {
	t.Parallel()
	if err := ExplainCredentialError(nil); err != nil {
		t.Fatalf("nil must stay nil, got %v", err)
	}
}
