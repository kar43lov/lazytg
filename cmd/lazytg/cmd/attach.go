package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kar43lov/lazytg/internal/app"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// attachTimeout caps how long the TUI waits for Telegram before opening on the
// local cache. Long enough for a handshake on a slow link, short enough that a
// dead network does not look like a hung binary.
const attachTimeout = 20 * time.Second

// dialogSyncTimeout bounds the chat-list walk. It runs in the background after
// the UI is up, so a stall costs freshness rather than startup.
const dialogSyncTimeout = 2 * time.Minute

// shutdownGrace is how long exit waits for the session goroutine to unwind
// after its context is cancelled. Short enough not to hold up quitting, long
// enough that the goroutine normally finishes before the database closes.
const shutdownGrace = 2 * time.Second

// errNotAuthorized means a session blob exists but Telegram rejected it —
// typically the session was revoked from another device.
var errNotAuthorized = errors.New("session is no longer authorized (run `lazytg login`)")

// attachTelegram brings the MTProto session up and wires the live services
// onto the runtime, then starts the chat-list sync.
//
// Every failure path here is non-fatal by design: the TUI is useful on the
// cached mirror alone, and a hard error would turn "no network" into "cannot
// start". Reasons are logged at warn so `--debug` explains an empty chat list.
//
// Returns after the session is usable, or after attachTimeout, whichever comes
// first. Blocking is deliberate — see the call site in runTUI.
//
// The returned function tears the connection down; it is never nil, so callers
// can defer it unconditionally. Ownership is explicit rather than relying on
// the parent context alone, which also keeps `go vet`'s lostcancel check happy.
func attachTelegram(ctx context.Context, rt *app.App, log *slog.Logger) (stop func()) {
	noop := func() {}

	phone, err := resolveAttachPhone(ctx, rt)
	if err != nil {
		log.Info("tui: opening on local cache only", "reason", err)
		return noop
	}

	_, secrets, err := resolvePaths()
	if err != nil {
		log.Warn("tui: cannot open secret store, offline", "err", err)
		return noop
	}

	// The dispatcher has to exist before the client: gotd takes its update
	// handler at construction time. Installing it on the App first makes
	// AttachClient adopt this instance instead of building a second one that
	// nothing feeds.
	dispatcher := tgclient.NewUpdatesDispatcher(rt.Bus, log)
	rt.Updates = dispatcher

	client, err := newClientWithUpdates(tgclient.NewSessionStore(secrets, phone), dispatcher.HandlerFunc())
	if err != nil {
		log.Warn("tui: cannot build telegram client, offline", "err", err)
		return noop
	}

	// attachCtx scopes the connection attempt so it can be abandoned on
	// timeout. Without that, a handshake completing after we gave up would call
	// AttachClient behind the already-built UI: the panes captured their
	// dependencies at construction and would never see the services, while the
	// writes themselves race with the reads that already happened. Worse, the
	// dialog sync would never start, leaving a connected session with a
	// permanently empty chat list. Abandoning is the determinate outcome.
	//
	// On success the cancel travels back to the caller as `stop` instead of
	// firing here — the session has to outlive this function.
	attachCtx, cancelAttach := context.WithCancel(ctx)

	// ready carries the outcome of the first connection attempt. Buffered so
	// the run goroutine never blocks on a send after we stopped waiting.
	ready := make(chan error, 1)
	// runDone closes when the session goroutine is fully gone, so shutdown can
	// wait for it. Without that wait, cancelling the context returns
	// immediately and the caller proceeds to close the SQLite handle while the
	// goroutine may still be persisting an update.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runErr := client.Run(attachCtx, func(runCtx context.Context) error {
			authorized, err := client.IsAuthorized(runCtx)
			if err != nil {
				ready <- fmt.Errorf("auth status: %w", err)
				return err
			}
			if !authorized {
				ready <- errNotAuthorized
				return errNotAuthorized
			}

			// Report readiness and let the caller do the attaching. Calling
			// AttachClient from here would race the timeout: cancelling a
			// context does not interrupt code already running, so a handshake
			// finishing just after the deadline would wire services onto the
			// App while the caller was already building the UI from the
			// offline values. The caller attaches only on the success branch
			// of its select, which closes that window by construction rather
			// than by a well-placed check.
			ready <- nil

			// Hold the connection open. Returning here would tear down the
			// session the services are about to bind to.
			<-runCtx.Done()
			return runCtx.Err()
		})
		// Run can fail before the callback ever executes (DNS, handshake,
		// migration). Report that instead of letting the wait time out.
		select {
		case ready <- runErr:
		default:
			// Nobody is waiting any more, so this is the session ending
			// mid-flight rather than a failed handshake. It has to be logged
			// here or it goes nowhere: reconnect is still a stub, so the
			// services stay bound to a dead client and the user sees sends
			// failing with no stated cause.
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				log.Warn("tui: telegram session ended — sends and live updates stop until restart",
					"err", runErr)
			}
		}
	}()

	select {
	case err := <-ready:
		if err != nil {
			cancelAttach()
			log.Warn("tui: telegram attach failed, opening on local cache", "err", err)
			return noop
		}
		// Wire the MTProto services here, on the goroutine that goes on to
		// build the UI. Everything the panes read is written before they are
		// constructed, so no synchronisation is needed and the timeout branch
		// cannot leave a half-attached App behind.
		rt.AttachClient(ctx, client)
	case <-time.After(attachTimeout):
		// A ready signal that landed in the same instant would otherwise be
		// thrown away: `select` picks randomly among ready cases, so a session
		// that connected right on the deadline could be torn down for no
		// reason. Check once more before giving up.
		select {
		case err := <-ready:
			if err == nil {
				rt.AttachClient(ctx, client)
				log.Info("tui: telegram session attached just before the deadline")
				startSync(ctx, rt, log)
				return sessionStopper(cancelAttach, runDone, log)
			}
			log.Warn("tui: telegram attach failed, opening on local cache", "err", err)
		default:
			log.Warn("tui: telegram did not connect in time, opening on local cache",
				"waited", attachTimeout)
		}
		cancelAttach()
		return noop
	case <-ctx.Done():
		cancelAttach()
		return noop
	}

	log.Info("tui: telegram session attached", "account", phone)
	startSync(ctx, rt, log)
	return sessionStopper(cancelAttach, runDone, log)
}

// sessionStopper builds the teardown closure: cancel the session context, then
// wait — briefly — for the goroutine to unwind. The wait matters because the
// caller closes the SQLite handle right after, and a goroutine still persisting
// an update would hit a closed database.
func sessionStopper(cancel context.CancelFunc, runDone <-chan struct{}, log *slog.Logger) func() {
	return func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(shutdownGrace):
			log.Warn("tui: telegram session did not stop in time", "waited", shutdownGrace)
		}
	}
}

// startSync kicks off the background work that fills the local mirror: the
// chat list first (nothing else has anything to show without it), then the
// per-chat history backfill queue.
func startSync(ctx context.Context, rt *app.App, log *slog.Logger) {
	if rt.Backfill != nil {
		rt.Backfill.Start(ctx)
	}
	if rt.Dialogs == nil {
		log.Warn("tui: dialogs service unavailable — chat list will stay as cached")
		return
	}
	go func() {
		syncCtx, cancel := context.WithTimeout(ctx, dialogSyncTimeout)
		defer cancel()
		stored, err := rt.Dialogs.Sync(syncCtx)
		switch {
		case err != nil && stored > 0:
			// Partial success is the common shape on FLOOD_WAIT: report what
			// landed so an incomplete list is explainable.
			log.Warn("tui: chat list sync incomplete", "chats", stored, "err", err)
		case err != nil:
			log.Warn("tui: chat list sync failed", "err", err)
		default:
			log.Info("tui: chat list synced", "chats", stored)
		}
	}()
}

// resolveAttachPhone picks the account to connect as: --account when given,
// otherwise the only logged-in one. With several accounts and no flag we
// refuse to guess — silently picking one would send messages from an account
// the user did not choose.
func resolveAttachPhone(ctx context.Context, rt *app.App) (string, error) {
	if flagAccount != "" {
		return flagAccount, nil
	}
	accounts, err := rt.Repo.GetAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("read accounts: %w", err)
	}
	switch len(accounts) {
	case 0:
		return "", errors.New("no account logged in — run `lazytg login --account +<phone>`")
	case 1:
		return accounts[0].Phone, nil
	default:
		return "", errors.New("several accounts logged in — pick one with --account +<phone>")
	}
}

// composerSender returns the live send service, or a nil interface when
// offline. Returning rt.Sender unconditionally would produce a non-nil
// interface wrapping a nil pointer, and the composer's `send == nil` guard
// would not catch it — the first Enter would panic.
func composerSender(rt *app.App) input.SendServiceInterface {
	if rt.Sender == nil {
		return nil
	}
	return rt.Sender
}

// threadHistoryProvider returns the live MTProto history fetcher, or a nil
// interface when offline. Same typed-nil hazard as composerSender.
func threadHistoryProvider(rt *app.App) thread.HistoryProvider {
	if rt.HistoryFetcher == nil {
		return nil
	}
	return rt.HistoryFetcher
}
