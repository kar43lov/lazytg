// Package app wires application components together via plain constructors (no DI framework).
//
// Build composes the storage-layer + bus + non-MTProto sync services so the
// rest of the binary (cmd/lazytg/cmd/tui.go) can compose a *bubbletea.Program
// on top. Components that require an active gotd connection (HistoryFetcher,
// Sender, UpdatesDispatcher, ReconnectManager, PollingFallback) are attached
// later via AttachClient — that lets the cmd layer manage the tg.Client.Run
// lifecycle (which expects to drive its own goroutine) without forcing every
// startup path through it (e.g. tests, lint runs).
//
// The struct fields are intentionally exported so cmd/lazytg/cmd/tui.go can
// hand them to internal/ui/app.New as Deps without an extra adapter layer.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/pgmac/lazytg/internal/core/config"
	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/core/files"
	coresync "github.com/pgmac/lazytg/internal/core/sync"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
	tgclient "github.com/pgmac/lazytg/internal/tg"
	"github.com/pgmac/lazytg/internal/ui/keymap"
)

// dbFileName is the SQLite filename inside Paths.Data. Mirrors the constant
// in cmd/lazytg/cmd/runtime.go — both layers must agree on file location for
// `lazytg login` and `lazytg` (TUI) to share state.
const dbFileName = "lazytg.db"

// Config bundles the boot-time options Build needs. Zero values pick the
// production defaults: paths resolved from XDG, keymap from defaults +
// Paths.Config/keymap.toml override, log level from the supplied logger.
//
// Tests typically supply a pre-resolved Paths and a *sqlite.Repo to skip the
// XDG lookup and migrations.
type Config struct {
	// Phone is the canonical E.164 phone of the active account. Used by
	// session lookups and as the status-bar alias placeholder until the
	// real alias arrives via accounts.getMe.
	Phone string

	// Polling forces history polling instead of updates.Manager. Set from
	// the --polling CLI flag in cmd/lazytg/cmd/root.go.
	Polling bool

	// Logger is the slog.Logger every wired component will share. Build
	// errors when nil — every callsite already has a logger via
	// PersistentPreRunE so no point making this implicit.
	Logger *slog.Logger

	// Paths overrides the XDG resolution. Zero value triggers config.Resolve.
	Paths config.Paths

	// KeymapPath overrides the keymap.toml location. Empty string falls
	// back to Paths.Config/keymap.toml; missing files are tolerated and
	// silently fall through to defaults.
	KeymapPath string
}

// App is the composed runtime. The *non-MTProto* services are populated by
// Build; the MTProto-aware ones (Sender, History, Updates, Reconnect) are
// nil until AttachClient is called from cmd/lazytg/cmd/tui.go.
//
// Close releases the storage handle. Background goroutines started via
// RunBackground are tied to the context passed there and clean themselves
// up on cancellation.
type App struct {
	Bus       *events.Bus
	Repo      *sqlite.Repo
	Peers     *sqlite.PeerRepo
	StateRepo *sqlite.StateRepo

	Live        *coresync.LiveService
	Degradation *coresync.DegradationDetector

	Keymap keymap.Keymap
	Log    *slog.Logger
	Phone  string
	Paths  config.Paths

	// MTProto-aware services. Populated by AttachClient. Nil-checked at
	// every consumer (cmd/tui.go falls back to a stubbed sender / history
	// when no client is attached, which is what the e2e tests rely on).
	Client    *tgclient.Client
	Sender    *coresync.SendService
	History   *coresync.HistoryService
	Backfill  *coresync.BackfillService
	Updates   *tgclient.UpdatesDispatcher
	Reconnect *coresync.ReconnectManager

	// Polling controls whether the cmd layer should engage PollingFallback
	// instead of subscribing to UpdatesDispatcher. The fallback itself is
	// constructed in cmd/tui.go because it takes the active-chat source
	// from the UI layer.
	Polling bool

	closed sync.Once
}

// Build composes the non-MTProto layer of the runtime. It opens the SQLite
// repo (running migrations as a side effect), wires the in-process bus,
// loads the keymap, and constructs the LiveService + DegradationDetector
// against the freshly-opened repo.
//
// Errors are wrapped with the offending step so log triage can locate the
// failure without re-running with --debug. Callers that fail at any stage
// after Open should call (*App).Close to release the SQLite handle.
func Build(ctx context.Context, cfg Config) (*App, error) {
	if cfg.Logger == nil {
		return nil, errors.New("app.Build: logger is required")
	}

	paths := cfg.Paths
	if paths == (config.Paths{}) {
		resolved, err := config.Resolve()
		if err != nil {
			return nil, fmt.Errorf("app.Build: resolve paths: %w", err)
		}
		paths = resolved
	}

	repo, err := sqlite.Open(ctx, filepath.Join(paths.Data, dbFileName))
	if err != nil {
		return nil, fmt.Errorf("app.Build: open repo: %w", err)
	}

	bus := events.New()
	peers := sqlite.NewPeerRepo(repo.DB())
	stateRepo := sqlite.NewStateRepo(repo.DB())

	live := coresync.NewLiveService(repo, bus, cfg.Logger)
	degradation := coresync.NewDegradationDetector(repo, bus, cfg.Logger, coresync.DegradationConfig{})

	km, err := loadKeymap(cfg.KeymapPath, paths.Config)
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("app.Build: keymap: %w", err)
	}

	return &App{
		Bus:         bus,
		Repo:        repo,
		Peers:       peers,
		StateRepo:   stateRepo,
		Live:        live,
		Degradation: degradation,
		Keymap:      km,
		Log:         cfg.Logger,
		Phone:       cfg.Phone,
		Paths:       paths,
		Polling:     cfg.Polling,
	}, nil
}

// loadKeymap picks the right keymap.toml path: explicit override wins,
// otherwise we look for Paths.Config/keymap.toml. A missing file falls
// through to defaults because keymap.Load already handles fs.ErrNotExist
// internally.
func loadKeymap(override, configDir string) (keymap.Keymap, error) {
	path := override
	if path == "" && configDir != "" {
		path = filepath.Join(configDir, "keymap.toml")
	}
	return keymap.Load(path)
}

// AttachClient wires the MTProto-aware services on top of an active
// *tg.Client + the persistent peer cache. After AttachClient the App is
// ready to drive sends, history fetches, updates dispatch and reconnect
// management — but the goroutines themselves still need RunBackground to
// fire (the cmd layer calls that once tea.Program is ready to receive
// program.Send for the bus → tea.Msg fan-in).
//
// bgCtx scopes the SendService deliver goroutines so an in-flight retry
// loop aborts cleanly on TUI shutdown. Pass the same ctx the cmd layer
// uses to drive the Bubble Tea program — typically the cobra cmd ctx
// (or its background sub-ctx). nil falls back to context.Background.
//
// The method is idempotent: calling it twice replaces the previous
// services with the new ones, which is useful when a reconnect rebuilds
// the underlying gotd client.
func (a *App) AttachClient(bgCtx context.Context, client *tgclient.Client) {
	if client == nil {
		return
	}
	a.Client = client

	historyFetcher := tgclient.NewHistoryFetcher(client.API())
	a.History = coresync.NewHistoryService(historyFetcher, peerLookupAdapter{peers: a.Peers}, a.Repo, a.Bus, a.Log)
	backfill, _ := coresync.NewBackfillService(a.History, a.Log, coresync.BackfillConfig{})
	a.Backfill = backfill

	sender := tgclient.NewSender(client.API(), peerResolverAdapter{peers: a.Peers})
	a.Sender = coresync.NewSendService(senderAdapter{sender: sender}, outgoingStoreAdapter{repo: a.Repo}, a.Bus, a.Log, coresync.SendConfig{}).
		WithBackgroundContext(bgCtx)

	a.Updates = tgclient.NewUpdatesDispatcher(a.Bus, a.Log)
	a.Reconnect = coresync.NewReconnectManager(reconnectAdapter{client: client}, a.Bus, a.Log, coresync.ReconnectConfig{})
}

// RunBackground spawns the long-running goroutines that don't need the
// MTProto session: the LiveService drain (bus → repo persistence) and the
// DegradationDetector probe loop. Returns a channel that closes once both
// goroutines exit (cleanly via ctx cancellation or otherwise) so callers
// can wait on shutdown without owning the wait group.
//
// The MTProto-aware goroutines (Backfill drain, Reconnect, Updates manager)
// are started by the cmd layer because they need to live inside the
// tg.Client.Run callback.
func (a *App) RunBackground(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup

	if a.Live != nil {
		wg.Add(1)
		liveDone := a.Live.Start(ctx)
		go func() {
			defer wg.Done()
			<-liveDone
		}()
	}
	if a.Degradation != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.Degradation.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.Log.Warn("degradation: run exited", "err", err)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// Close releases the storage handle. Safe to call multiple times — the
// underlying *sql.DB.Close is idempotent and we guard with sync.Once so a
// failed Build that returns *App+error from a future refactor doesn't
// double-close.
func (a *App) Close() error {
	var err error
	a.closed.Do(func() {
		if a.Repo != nil {
			err = a.Repo.Close()
		}
	})
	return err
}

// DedupStoreAdapter bridges *sqlite.Repo into core/files.DedupStore.
// The repo speaks sqlite.DownloadedFile (with a wall-clock timestamp and
// access_hash typed as int64); core/files.DedupRecord is the gotd-free
// view that DownloadService needs. The adapter translates between the
// two without forcing either side to know about the other's package.
//
// Exported because Task 10 wiring (cmd/lazytg/cmd/tui.go) constructs
// the *core/files.DownloadService at TUI start-up time and needs to
// pass the adapter through. Pinning the type here keeps the storage
// → core mapping co-located with the rest of the wire-time adapters.
type DedupStoreAdapter struct {
	Repo *sqlite.Repo
}

// GetDownloadedPath delegates to the underlying repo.
func (a DedupStoreAdapter) GetDownloadedPath(ctx context.Context, fileID int64) (string, int64, bool, error) {
	return a.Repo.GetDownloadedPath(ctx, fileID)
}

// SaveDownloadedFile translates a gotd-free DedupRecord into the
// repo's storage type and persists it.
func (a DedupStoreAdapter) SaveDownloadedFile(ctx context.Context, f files.DedupRecord) error {
	return a.Repo.SaveDownloadedFile(ctx, sqlite.DownloadedFile{
		FileID:     f.FileID,
		AccessHash: f.AccessHash,
		Path:       f.Path,
		Size:       f.Size,
	})
}

// Compile-time anchor: every DedupStoreAdapter value satisfies the
// core/files.DedupStore contract. Triggers a build break if the
// interface drifts (e.g. a new method) so the adapter gets updated
// alongside the contract.
var _ files.DedupStore = (*DedupStoreAdapter)(nil)

// senderAdapter bridges *tg.Sender (returns serverID int64) into the
// coresync.SenderInterface signature. The signatures already match, but
// declaring the wrapper explicitly keeps the import direction (core → tg
// is forbidden) trivially satisfied without compile-time tricks.
type senderAdapter struct {
	sender *tgclient.Sender
}

func (a senderAdapter) SendText(ctx context.Context, chatID int64, text string, replyTo int) (int64, error) {
	return a.sender.SendText(ctx, chatID, text, replyTo)
}

// outgoingStoreAdapter bridges *sqlite.Repo (which speaks
// sqlite.OutgoingMessage) into the gotd-free OutgoingStore contract used by
// coresync.SendService (which speaks coresync.OutgoingRecord). The two
// shapes are field-for-field equivalent — the adapter just translates
// between the layer-specific value types so neither side imports the
// other.
type outgoingStoreAdapter struct {
	repo *sqlite.Repo
}

func (a outgoingStoreAdapter) SaveOutgoing(ctx context.Context, m coresync.OutgoingRecord) error {
	return a.repo.SaveOutgoing(ctx, sqlite.OutgoingMessage{
		LocalID:   m.LocalID,
		ChatID:    m.ChatID,
		Text:      m.Text,
		ReplyTo:   m.ReplyTo,
		State:     m.State,
		CreatedAt: m.CreatedAt,
	})
}

func (a outgoingStoreAdapter) UpdateOutgoingState(ctx context.Context, localID, state string, serverID int64, errMsg string) error {
	return a.repo.UpdateOutgoingState(ctx, localID, state, serverID, errMsg)
}

// peerLookupAdapter bridges *sqlite.PeerRepo into coresync.PeerLookup. The
// storage layer returns *sqlite.ErrPeerNotFound; we translate that into
// coresync.ErrPeerUnknown so the history service can branch on a
// gotd-free sentinel.
type peerLookupAdapter struct {
	peers *sqlite.PeerRepo
}

func (a peerLookupAdapter) Lookup(ctx context.Context, chatID int64) (coresync.PeerInfo, error) {
	p, err := a.peers.Get(ctx, chatID)
	if err != nil {
		if errors.Is(err, sqlite.ErrPeerNotFound) {
			return coresync.PeerInfo{}, coresync.ErrPeerUnknown
		}
		return coresync.PeerInfo{}, err
	}
	return coresync.PeerInfo{
		AccessHash: p.AccessHash,
		Type:       string(p.Type),
	}, nil
}

// peerResolverAdapter bridges *sqlite.PeerRepo into tg.PeerResolver. Same
// shape as peerLookupAdapter but lives in the tg-side contract because
// Sender consumes domain.Peer directly.
type peerResolverAdapter struct {
	peers *sqlite.PeerRepo
}

func (a peerResolverAdapter) Resolve(ctx context.Context, chatID int64) (domain.Peer, error) {
	return a.peers.Get(ctx, chatID)
}

// reconnectAdapter bridges *tg.Client into coresync.ReconnectClient. The
// real Connect/Ping operations need to be issued from inside Client.Run
// — until we plumb that, AttachClient gets a stub that delegates Connect
// to a no-op (the cmd layer drives the real Run loop) and Ping to a
// presence check via Auth().Status. This is enough for ReconnectManager
// to keep state-machine logic correct; full reconnect orchestration lives
// in cmd/tui.go.
type reconnectAdapter struct {
	client *tgclient.Client
}

func (a reconnectAdapter) Connect(_ context.Context) error {
	// The actual connect is owned by the cmd layer via tg.Client.Run.
	// Reconnect inside an active session is a gotd-internal concern; the
	// adapter exists so ReconnectManager has a non-nil dependency that
	// returns nil quickly when the session is up.
	return nil
}

func (a reconnectAdapter) Ping(ctx context.Context) error {
	if a.client == nil {
		return errors.New("reconnect: client not attached")
	}
	_, err := a.client.IsAuthorized(ctx)
	return err
}

func (a reconnectAdapter) OnDisconnect() <-chan error {
	if a.client == nil {
		c := make(chan error)
		return c
	}
	return a.client.OnDisconnect()
}
