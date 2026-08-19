package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"

	"github.com/kar43lov/lazytg/internal/app"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
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

	// The update handler is the manager when one can be built, and the bare
	// dispatcher otherwise. The manager is a strict superset: without its Run
	// it forwards updates exactly as the dispatcher does, and with it they
	// arrive ordered and gaps are closed through getDifference.
	handler, manager := updateHandler(rt, dispatcher, log)

	client, err := newClientWithUpdates(tgclient.NewSessionStore(secrets, phone), handler)
	if err != nil {
		log.Warn("tui: cannot build telegram client, offline", "err", err)
		return noop
	}

	// The run loop lives in a supervisor rather than a bare goroutine so a
	// session that dies later can be stood back up. See session.go.
	supervisor := newSessionSupervisor(ctx, client, log)
	if manager != nil {
		supervisor.gapRecovery = func(runCtx context.Context) error {
			return client.RunGapRecovery(runCtx, manager)
		}
	}
	if err := supervisor.Start(ctx, attachTimeout); err != nil {
		log.Warn("tui: telegram attach failed, opening on local cache", "err", err)
		return noop
	}

	// Wire the MTProto services here, on the goroutine that goes on to build
	// the UI. Everything the panes read is written before they are
	// constructed, so no synchronisation is needed and a failed attach cannot
	// leave a half-attached App behind.
	rt.AttachClient(ctx, client, app.WithReconnector(supervisor.Restart))

	log.Info("tui: telegram session attached", "account", phone)
	startReconnect(ctx, rt, log)
	startRead(ctx, rt, log)
	startPolling(ctx, rt, log)
	startSync(ctx, rt, log)
	return supervisor.Stop
}

// updateHandler picks what the client hands updates to. It returns the
// manager separately because the caller has to start it from inside the
// session, once the connection is up and the account id can be asked for.
//
// A nil manager is not an error state: it means the update state storage is
// unavailable, and lazytg then behaves as it did for its whole first
// release — updates are delivered as they arrive, and anything missed during
// an outage is picked up by the dialog sync and the freshness check instead
// of by getDifference.
func updateHandler(rt *app.App, dispatcher *tgclient.UpdatesDispatcher, log *slog.Logger) (telegram.UpdateHandler, *updates.Manager) {
	manager := rt.UpdatesManager(dispatcher)
	if manager == nil {
		log.Warn("tui: no update state storage — live updates run without gap recovery")
		return dispatcher.HandlerFunc(), nil
	}
	return manager, manager
}

// startReconnect runs the reconnect state machine for the lifetime of the
// session. It was built by AttachClient from the first commit that introduced
// it and started by nobody, which made the whole thing inert: no reconnect
// after a drop, and — since ReconnectManager is the only publisher of
// events.ConnectionStateChanged — a connection indicator frozen on whatever
// value the initial attach put there.
func startReconnect(ctx context.Context, rt *app.App, log *slog.Logger) {
	if rt.Reconnect == nil {
		log.Warn("tui: reconnect manager unavailable — a dropped session will not come back")
		return
	}
	go func() {
		if err := rt.Reconnect.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("tui: reconnect manager exited", "err", err)
		}
	}()
}

// startRead runs the service that tells Telegram which chats the user has
// read. Without it a conversation opened here stays unread on every other
// device — and an account that reads without ever acknowledging is a pattern
// no ordinary client produces.
func startRead(ctx context.Context, rt *app.App, log *slog.Logger) {
	if rt.Read == nil {
		return
	}
	_ = rt.Read.Start(ctx)
	log.Debug("tui: read receipts engaged")
}

// startPolling engages the history-polling fallback, which exists only when
// --polling was passed. Until this call the flag was a no-op: it reached
// app.Config and stopped there, so a user who set it because live updates
// were not arriving got exactly the behaviour they were trying to work
// around, with no indication that nothing had changed.
//
// The fallback runs alongside the live dispatcher rather than replacing it.
// The push path is the one that delivers within a second, and polling three
// chats every three seconds is a net for what a gap-prone connection drops
// silently — not a substitute for updates.
func startPolling(ctx context.Context, rt *app.App, log *slog.Logger) {
	if rt.PollingSvc == nil {
		return
	}
	log.Info("tui: history polling engaged", "interval", tgclient.DefaultPollingInterval)
	go func() {
		if err := rt.PollingSvc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("tui: polling fallback exited", "err", err)
		}
	}()
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
	// Built here rather than in AttachClient because it needs Dialogs, and
	// this is the first place that exists.
	rt.Rediscover = coresync.NewRediscoverer(rt.Dialogs, rt.Bus, log, 0, dialogSyncTimeout)
	_ = rt.Rediscover.Start(ctx)
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
