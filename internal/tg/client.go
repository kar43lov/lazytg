package tg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Environment variable names for the Telegram API credentials. We keep them
// here so every entry-point reads from the same source of truth.
const (
	EnvAPIID   = "LAZYTG_API_ID"
	EnvAPIHash = "LAZYTG_API_HASH"
)

// Build-time credentials, injected by GoReleaser via -ldflags from repository
// secrets (see .goreleaser.yaml). They are empty in every build made from the
// source tree, which is the point: Telegram blocks an api_id that appears in
// public source with API_ID_PUBLISHED_FLOOD, so the value may live in CI
// secrets and in shipped binaries but never in this repository.
//
// Kept as strings rather than an int because -ldflags -X can only write to
// string variables; ResolveCredentials does the conversion.
var (
	embeddedAPIID   string
	embeddedAPIHash string
)

// CredentialsSource records which layer supplied the credentials actually in
// use. `lazytg version` prints it (never the values) so a user hitting an
// api_id-level ban can tell at a glance whether they are on the shipped
// release key or on their own.
type CredentialsSource string

// The credential layers, in descending precedence. SourceNone means no layer
// supplied usable credentials.
const (
	SourceFlags    CredentialsSource = "flags"
	SourceEnv      CredentialsSource = "env"
	SourceEmbedded CredentialsSource = "embedded"
	SourceNone     CredentialsSource = "none"
)

// ErrNoCredentials is returned when no layer supplied credentials — the normal
// case for a binary built from source, where the embedded values are empty.
// The message doubles as the user-facing remediation because this is the first
// wall a self-builder hits: the CLI prints it verbatim, so it is deliberately
// multi-line and capitalised, which is what the exemption below is for.
//
//nolint:staticcheck // ST1005: multi-line user-facing remediation text
var ErrNoCredentials = fmt.Errorf(
	"no Telegram API credentials available\n"+
		"  This binary was built from source, so it carries no embedded api_id.\n"+
		"  Create an application at https://my.telegram.org/apps and export:\n"+
		"    export %s=1234567\n"+
		"    export %s=<32-hex api_hash>\n"+
		"  Official release binaries ship with credentials already embedded.",
	EnvAPIID, EnvAPIHash)

// ClientConfig is the dependency-injection bag for tg.Client construction.
// All fields are mandatory; gotd's own logger is left at its default zap.Nop
// for Stage 1 — wiring an slog→zap adapter is deferred until we actually need
// transport-level logs (Stage 2 sync work).
type ClientConfig struct {
	APIID        int
	APIHash      string
	SessionStore session.Storage

	// UpdateHandler receives live MTProto updates. Optional: leaving it nil
	// yields a client that can read and send but never learns about incoming
	// messages. gotd only accepts the handler at construction time, so the
	// dispatcher has to exist before the client does — which is why this is a
	// config field rather than something AttachClient could set later.
	UpdateHandler telegram.UpdateHandler
}

// Client is the lazytg-specific wrapper around gotd's *telegram.Client. The
// wrapper exists so the rest of the code base never imports gotd packages
// directly — the depguard rules forbid `internal/core` from doing so.
//
// disconnect is the buffered (cap=1) channel that surfaces transport-level
// failures from the gotd Run loop. ReconnectManager (core/sync) listens on
// OnDisconnect to schedule reconnection with exponential backoff. The
// channel is buffered so a Run that errors before anyone reads cannot
// deadlock; only the most recent error is preserved (subsequent writes
// non-blocking-drop when the buffer is full — newer error wins).
type Client struct {
	tg         *telegram.Client
	disconnect chan error
	states     chan string
}

// New constructs a Client from the given ClientConfig. It does not connect to
// Telegram — call Run to do that.
func New(cfg ClientConfig) (*Client, error) {
	if cfg.APIID == 0 || cfg.APIHash == "" {
		// Callers are expected to come through ResolveCredentials, which
		// already produces the actionable message; this is the last-ditch
		// guard for a caller that built the config by hand.
		return nil, fmt.Errorf("api credentials missing (see ResolveCredentials: flags, %s/%s, or a release build)", EnvAPIID, EnvAPIHash)
	}
	if cfg.SessionStore == nil {
		return nil, errors.New("session storage is required")
	}
	c := &Client{
		disconnect: make(chan error, 1),
		states:     make(chan string, 1),
	}
	c.tg = telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: cfg.SessionStore,
		UpdateHandler:  cfg.UpdateHandler,
		// gotd reconnects on its own while Run is executing, and until
		// v0.161 it did so silently: the status bar could sit on "online"
		// through an entire outage because the only thing that ever moved
		// it was a Run that returned. This callback is the transport
		// telling us what it is actually doing.
		OnConnectionState: c.onConnectionState,
	})
	return c, nil
}

// onConnectionState translates gotd's connection lifecycle into the state
// vocabulary the rest of lazytg already speaks, then hands it to whoever
// reads ConnectionStates. gotd documents this callback as called
// synchronously from the connection lifecycle and forbids blocking in it,
// which is why the send is non-blocking and drops stale values instead of
// waiting for a reader.
func (c *Client) onConnectionState(state telegram.ConnectionState) {
	name := connectionStateName(state)
	if name == "" {
		// An enum value this build does not know. Publishing "unknown"
		// would put a word in the status bar that means nothing to the
		// user; saying nothing leaves the last real state on screen.
		return
	}
	pushLatestString(c.states, name)
}

// connectionStateName maps telegram.ConnectionState onto the strings in
// core/sync, which the status bar renders verbatim. Unknown values map to
// the empty string so callers can skip them explicitly.
func connectionStateName(state telegram.ConnectionState) string {
	switch state {
	case telegram.ConnectionStateReady:
		return coresync.ConnectionStateOnline
	case telegram.ConnectionStateConnecting:
		return coresync.ConnectionStateConnecting
	case telegram.ConnectionStateDisconnected:
		return coresync.ConnectionStateOffline
	default:
		return ""
	}
}

// ConnectionStates yields transport-level state transitions observed by the
// underlying gotd client, satisfying coresync.ConnectionStateReporter. The
// channel keeps only the most recent value: a status indicator cares about
// where the connection is now, not about the sequence it took to get there,
// and a slow reader must never stall gotd's connection lifecycle. The
// channel is never closed.
func (c *Client) ConnectionStates() <-chan string { return c.states }

// ResolveCredentials picks the credentials to run with, checking three layers
// in descending precedence: explicit flags, environment variables, values
// embedded at build time. The first layer that is touched at all wins — a
// half-filled layer is an error rather than a silent fall-through to the next
// one, because "I set LAZYTG_API_ID and it still used the release key" is a
// failure mode that surfaces only as an unexplained ban weeks later.
//
// The returned CredentialsSource is safe to log and display; the credentials
// themselves are not.
func ResolveCredentials(flagID, flagHash string) (apiID int, apiHash string, src CredentialsSource, err error) {
	if flagID != "" || flagHash != "" {
		id, hash, err := parseCredentialPair(flagID, flagHash, "--api-id", "--api-hash")
		if err != nil {
			return 0, "", SourceNone, err
		}
		return id, hash, SourceFlags, nil
	}

	envID, envHash := os.Getenv(EnvAPIID), os.Getenv(EnvAPIHash)
	if envID != "" || envHash != "" {
		id, hash, err := parseCredentialPair(envID, envHash, EnvAPIID, EnvAPIHash)
		if err != nil {
			return 0, "", SourceNone, err
		}
		return id, hash, SourceEnv, nil
	}

	if embeddedAPIID != "" || embeddedAPIHash != "" {
		id, hash, err := parseCredentialPair(embeddedAPIID, embeddedAPIHash,
			"embedded api_id", "embedded api_hash")
		if err != nil {
			// A release built with a broken secret is a build bug, not user
			// error — say so, otherwise the user hunts their own config.
			return 0, "", SourceNone, fmt.Errorf("build-time credentials are malformed (report this): %w", err)
		}
		return id, hash, SourceEmbedded, nil
	}

	return 0, "", SourceNone, ErrNoCredentials
}

// HasEmbeddedCredentials reports whether this binary was built with baked-in
// credentials. Used by `lazytg version` to describe the build without
// resolving (and thus without caring about the current env).
func HasEmbeddedCredentials() bool {
	return embeddedAPIID != "" && embeddedAPIHash != ""
}

// parseCredentialPair validates one credential layer. idName/hashName are the
// user-visible names of the inputs so the error tells people exactly which
// knob to turn.
func parseCredentialPair(rawID, hash, idName, hashName string) (int, string, error) {
	if rawID == "" {
		return 0, "", fmt.Errorf("%s is set but %s is empty — both are required", hashName, idName)
	}
	if hash == "" {
		return 0, "", fmt.Errorf("%s is set but %s is empty — both are required", idName, hashName)
	}
	apiID, err := strconv.Atoi(strings.TrimSpace(rawID))
	if err != nil {
		// The offending value is deliberately not echoed. Swapping the two
		// variables is a common mistake, which would put the api_hash in
		// this message — and cobra prints it straight to stderr, bypassing
		// the slog redaction handler that scrubs hex secrets from logs.
		return 0, "", fmt.Errorf("%s: want an integer (did you swap it with %s?)", idName, hashName)
	}
	if apiID <= 0 {
		return 0, "", fmt.Errorf("%s: want a positive integer, got %d", idName, apiID)
	}
	return apiID, strings.TrimSpace(hash), nil
}

// Raw exposes the underlying *telegram.Client so the tg package's own helper
// files (auth.go, future history.go, …) can use it without making the field
// public to other packages — the wrapper itself stays unexported across
// package boundaries by convention.
func (c *Client) Raw() *telegram.Client { return c.tg }

// API returns the raw RPC client used to issue typed MTProto calls
// (messages.getHistory, messages.sendMessage, …). The Stage 2 sync helpers
// in this package call it to keep `internal/core` free of gotd imports.
func (c *Client) API() *tg.Client { return c.tg.API() }

// Run starts the MTProto session and blocks until fn returns or ctx is
// cancelled. fn is invoked with a sub-context that is cancelled when the
// connection is torn down.
//
// On return, Run forwards the terminal error (or nil) on the disconnect
// channel using a non-blocking send. Cancellation due to ctx is forwarded
// as well — ReconnectManager treats context.Canceled as "user-initiated
// shutdown, do not reconnect". The send is non-blocking so a missing
// listener cannot stall the close path; the buffer (cap=1) keeps the
// latest signal and drops earlier ones.
func (c *Client) Run(ctx context.Context, fn func(ctx context.Context) error) error {
	err := c.tg.Run(ctx, fn)
	c.signalDisconnect(err)
	return err
}

// pushLatestString performs a non-blocking write that keeps the newest value:
// on a full buffer the stale entry is drained first. Used for the connection
// state feed, where a reader that fell behind wants the current state rather
// than a replay of an outage it already missed.
func pushLatestString(ch chan string, v string) {
	select {
	case ch <- v:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}

// signalDisconnect performs a non-blocking write to the disconnect channel.
// If a previous error has not yet been read it is dropped in favour of the
// new one — listeners only ever care about the most recent failure.
func (c *Client) signalDisconnect(err error) {
	select {
	case c.disconnect <- err:
	default:
		// Buffer full: drain stale signal so newer one can land.
		select {
		case <-c.disconnect:
		default:
		}
		select {
		case c.disconnect <- err:
		default:
		}
	}
}

// OnDisconnect returns a receive-only channel that yields the terminal
// error of the most recent Run call. Multiple consumers must coordinate
// externally — the channel has a single buffer slot. The channel is never
// closed; consumers should select on a context.Done() in parallel.
func (c *Client) OnDisconnect() <-chan error { return c.disconnect }

// RunGapRecovery starts the update manager's state machine and blocks until
// ctx is cancelled. It must be called from inside Client.Run — it needs an
// active connection both for accounts.getSelf and for the getDifference
// calls the manager issues.
//
// Failure here is not fatal to the session: a manager whose Run never
// succeeded passes updates through to the handler as they arrive, which is
// precisely the behaviour lazytg had before the manager was wired. The
// caller is expected to log and carry on rather than tear the session down.
func (c *Client) RunGapRecovery(ctx context.Context, m *updates.Manager) error {
	self, err := c.tg.Self(ctx)
	if err != nil {
		return fmt.Errorf("updates: self: %w", err)
	}
	return RunManager(ctx, m, c.API(), self.ID)
}

// RunManager drives one manager run and blocks until ctx is cancelled. It is
// separate from RunGapRecovery only so it can be exercised without a live
// connection — everything below the accounts.getSelf call is here.
//
// The Reset is load-bearing. A manager keeps its in-memory state after Run
// returns and nothing in gotd clears it, so the second Run on the same
// instance fails with "already authorized (userID: N)" — and the manager is
// the client's update handler, fixed at construction, so a session restart
// cannot simply build a new one. Without this call, gap recovery would work
// exactly until the first reconnect and then be silently dead for the rest
// of the process. Reset drops the in-memory state only: the pts/qts rows in
// SQLite are untouched, so the new run resumes where the old one stopped.
func RunManager(ctx context.Context, m *updates.Manager, api updates.API, selfID int64) error {
	if m == nil {
		return errors.New("updates: no manager")
	}
	m.Reset()
	return m.Run(ctx, api, selfID, updates.AuthOptions{})
}

// IsAuthorized reports whether the persisted session is still valid for use.
// Must be called from inside Client.Run because gotd needs an active
// connection to ask the server.
func (c *Client) IsAuthorized(ctx context.Context) (bool, error) {
	status, err := c.tg.Auth().Status(ctx)
	if err != nil {
		return false, fmt.Errorf("auth status: %w", err)
	}
	return status.Authorized, nil
}
