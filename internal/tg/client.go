package tg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// Environment variable names for the Telegram API credentials. We keep them
// here so every entry-point reads from the same source of truth.
const (
	EnvAPIID   = "LAZYTG_API_ID"
	EnvAPIHash = "LAZYTG_API_HASH"
)

// ClientConfig is the dependency-injection bag for tg.Client construction.
// All fields are mandatory; gotd's own logger is left at its default zap.Nop
// for Stage 1 — wiring an slog→zap adapter is deferred until we actually need
// transport-level logs (Stage 2 sync work).
type ClientConfig struct {
	APIID        int
	APIHash      string
	SessionStore session.Storage
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
}

// New constructs a Client from the given ClientConfig. It does not connect to
// Telegram — call Run to do that.
func New(cfg ClientConfig) (*Client, error) {
	if cfg.APIID == 0 || cfg.APIHash == "" {
		return nil, fmt.Errorf("api credentials missing (set %s and %s)", EnvAPIID, EnvAPIHash)
	}
	if cfg.SessionStore == nil {
		return nil, errors.New("session storage is required")
	}
	tgClient := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: cfg.SessionStore,
	})
	return &Client{
		tg:         tgClient,
		disconnect: make(chan error, 1),
	}, nil
}

// CredentialsFromEnv reads APIID/APIHash from the standard env vars. Returns
// a friendly error pointing at the variable names rather than just whatever
// strconv produces, because new contributors hit this most often.
func CredentialsFromEnv() (apiID int, apiHash string, err error) {
	raw := os.Getenv(EnvAPIID)
	if raw == "" {
		return 0, "", fmt.Errorf("env %s is empty", EnvAPIID)
	}
	apiID, err = strconv.Atoi(raw)
	if err != nil {
		return 0, "", fmt.Errorf("env %s: parse int: %w", EnvAPIID, err)
	}
	apiHash = os.Getenv(EnvAPIHash)
	if apiHash == "" {
		return 0, "", fmt.Errorf("env %s is empty", EnvAPIHash)
	}
	return apiID, apiHash, nil
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
