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
	"github.com/pgmac/lazytg/internal/core/obs"
	"github.com/pgmac/lazytg/internal/core/search"
	"github.com/pgmac/lazytg/internal/core/security"
	coresync "github.com/pgmac/lazytg/internal/core/sync"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
	tgclient "github.com/pgmac/lazytg/internal/tg"
	"github.com/pgmac/lazytg/internal/ui/keymap"
	"github.com/pgmac/lazytg/internal/ui/palette"
)

// dbFileName is the SQLite filename inside Paths.Data. Mirrors the constant
// in cmd/lazytg/cmd/runtime.go — both layers must agree on file location for
// `lazytg login` and `lazytg` (TUI) to share state.
const dbFileName = "lazytg.db"

// secretsFileName mirrors internal/core/config.secretsFileName so the
// startup audit knows where the age-encrypted fallback secrets file
// lives. Kept as a string constant (not an exported config helper)
// because the file is optional — when the OS keyring is available
// nothing is ever written to disk and the audit produces a "missing"
// finding (which is informational, not fatal).
const secretsFileName = "secrets.age"

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

	// SendRateLimit overrides the outbound send-rate ceiling. The zero
	// value picks security.DefaultSendRate (10/sec) and
	// security.DefaultSendBurst (30) — the project-wide ban-risk floor.
	// Tests that exercise SendService directly can disable the guard by
	// leaving cfg untouched and constructing without AttachClient (the
	// guard only activates once SendService is wired in AttachClient).
	SendRateLimit security.SendRateLimit

	// SkipPermissionsAudit disables the startup permissions audit
	// (CheckAtStartup + EnforceFatal). Default false. Used by tests
	// that point at temp-dir XDG paths whose modes they intentionally
	// don't control — e.g. CI runners where umask drift would
	// otherwise flake the audit. Production wiring leaves this at
	// false so a tampered DB / secrets file aborts the boot.
	SkipPermissionsAudit bool
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

	// SendGuard is the project-wide outbound rate-limit (10 msg/sec by
	// default). Constructed during Build so AttachClient can plug it
	// into SendService. Exposed as a public field so cmd/lazytg/cmd/tui.go
	// can also pass it through to any future per-action consumer
	// (e.g. file-upload throttling).
	SendGuard *security.SendGuard

	// Stage 3 search pipeline. All four are storage-only and live for
	// the lifetime of the App so the TUI can keep a long-lived service
	// instance: Indexer rebuilds on demand, ReindexSvc walks the chat
	// list, LazyIndex fires the first-search trigger, SearchSvc
	// answers queries.
	Indexer    *search.Indexer
	ReindexSvc *search.ReindexService
	LazyIndex  *search.LazyTrigger
	SearchSvc  *search.Service

	// Frecency feeds the command palette (top chats by recency × visit
	// count) and is updated on every palette-driven chat switch.
	Frecency palette.FrecencyStore

	// FileStore + DedupCache live for the lifetime of the App so the
	// download/upload services can be rebuilt on AttachClient without
	// reallocating disk-state.
	FileStore *files.FileStore
	Dedup     *files.DedupCache

	// DBSizeMonitor warns the UI through the bus when the SQLite file
	// crosses the 1 GiB threshold. Started by RunBackground.
	DBSizeMonitor *obs.DBSizeMonitor

	// MTProto-aware services. Populated by AttachClient. Nil-checked at
	// every consumer (cmd/tui.go falls back to a stubbed sender / history
	// when no client is attached, which is what the e2e tests rely on).
	Client      *tgclient.Client
	Sender      *coresync.SendService
	History     *coresync.HistoryService
	Backfill    *coresync.BackfillService
	Updates     *tgclient.UpdatesDispatcher
	Reconnect   *coresync.ReconnectManager
	DownloadSvc *files.DownloadService
	UploadSvc   *files.UploadService

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

	// Permissions audit BEFORE sqlite.Open so a tampered lazytg.db (with
	// wrong mode bits) is caught before sqlite.Open silently chmod's it
	// back to 0600. The audit emits "missing" findings on first run for
	// secrets.age + lazytg.db; both are warn-class, so EnforceFatal lets
	// the boot proceed.
	if !cfg.SkipPermissionsAudit {
		if err := runStartupPermissionsAudit(paths, cfg.Logger); err != nil {
			return nil, fmt.Errorf("app.Build: %w", err)
		}
	}

	repo, err := sqlite.Open(ctx, filepath.Join(paths.Data, dbFileName))
	if err != nil {
		return nil, fmt.Errorf("app.Build: open repo: %w", err)
	}

	guard, err := security.NewSendGuard(cfg.SendRateLimit)
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("app.Build: send guard: %w", err)
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

	// Stage 3 search pipeline: Indexer is the dumb sql writer, Reindex
	// walks chat ids through it, LazyIndex fires the first-search
	// background pass, Service answers queries. NewIndexer takes the
	// repo directly because *sqlite.Repo satisfies search.IndexStore via
	// DB().
	indexer := search.NewIndexer(repo, cfg.Logger)
	reindexSvc := search.NewReindexService(indexer, repo, bus, cfg.Logger, search.DefaultPerChatLimit)
	lazyIndex := search.NewLazyTrigger(reindexSvc, cfg.Logger)
	searchSvc := search.NewService(repo, lazyIndex, cfg.Logger)

	frecency := palette.NewRepoStore(repo)

	// FileStore + DedupCache: both are storage-only so they live in
	// Build. The download / upload services need the gotd Downloader /
	// Uploader and are deferred to AttachClient.
	fileStore, err := files.NewFileStoreDefault(cfg.Logger)
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("app.Build: file store: %w", err)
	}
	dedup, err := files.NewDedupCache(DedupStoreAdapter{Repo: repo})
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("app.Build: dedup cache: %w", err)
	}

	dbSizeMonitor := obs.NewDBSizeMonitor(repo, bus, cfg.Logger, obs.DBSizeConfig{})

	return &App{
		Bus:           bus,
		Repo:          repo,
		Peers:         peers,
		StateRepo:     stateRepo,
		Live:          live,
		Degradation:   degradation,
		Keymap:        km,
		Log:           cfg.Logger,
		Phone:         cfg.Phone,
		Paths:         paths,
		SendGuard:     guard,
		Indexer:       indexer,
		ReindexSvc:    reindexSvc,
		LazyIndex:     lazyIndex,
		SearchSvc:     searchSvc,
		Frecency:      frecency,
		FileStore:     fileStore,
		Dedup:         dedup,
		DBSizeMonitor: dbSizeMonitor,
		Polling:       cfg.Polling,
	}, nil
}

// runStartupPermissionsAudit runs the canonical Stage 3 audit set
// (secrets.age, ConfigDir, ConfigDir/.../lazytg.db, StateDir) and either
// returns a wrapped error (fail-class findings — boot must abort) or
// logs a single warn line per warn-class finding. Pulled out of Build
// so the audit list lives in one place and tests can call it directly.
func runStartupPermissionsAudit(paths config.Paths, log *slog.Logger) error {
	checks := []security.PathCheck{
		{
			Path:         filepath.Join(paths.Config, secretsFileName),
			Type:         security.KindFile,
			ExpectedMode: 0o600,
			Severity:     security.SeverityFail,
		},
		{
			Path:         paths.Config,
			Type:         security.KindDir,
			ExpectedMode: 0o700,
			Severity:     security.SeverityWarn,
		},
		{
			Path:         filepath.Join(paths.Data, dbFileName),
			Type:         security.KindFile,
			ExpectedMode: 0o600,
			Severity:     security.SeverityFail,
		},
		{
			Path:         paths.State,
			Type:         security.KindDir,
			ExpectedMode: 0o700,
			Severity:     security.SeverityWarn,
		},
	}
	issues := security.CheckAtStartup(checks)
	for _, issue := range security.FilterBySeverity(issues, security.SeverityWarn) {
		log.Warn("security: startup audit warning",
			"path", issue.Path,
			"category", string(issue.Category),
			"expected_mode", issue.Expected,
			"actual_mode", issue.Actual,
			"message", issue.Message,
		)
	}
	return security.EnforceFatal(issues)
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
		WithBackgroundContext(bgCtx).
		WithRateLimiter(a.SendGuard)

	a.Updates = tgclient.NewUpdatesDispatcher(a.Bus, a.Log)
	a.Reconnect = coresync.NewReconnectManager(reconnectAdapter{client: client}, a.Bus, a.Log, coresync.ReconnectConfig{})

	// Stage 3 file pipelines: tg.Downloader streams bytes, tg.Uploader
	// + Sender feed the FilesAdapter that satisfies both files.TGUploader
	// and files.SendMediaSender contracts. Build errors are logged-and-
	// swallowed because a nil DownloadSvc/UploadSvc just makes the
	// corresponding hotkey a quiet no-op (the same fallback the UI app
	// honours when no client is attached at all).
	tgDownloader := tgclient.NewDownloader(client.API(), a.Log)
	if dl, err := files.NewDownloadService(tgDownloader, a.FileStore, a.Dedup, a.Bus, a.Log); err != nil {
		a.Log.Warn("attach: download service init failed", "err", err)
	} else {
		a.DownloadSvc = dl
	}

	tgUploader := tgclient.NewUploader(client.API(), a.Log)
	filesAdapter, err := tgclient.NewFilesAdapter(tgUploader, sender)
	if err != nil {
		a.Log.Warn("attach: files adapter init failed", "err", err)
		return
	}
	if up, err := files.NewUploadService(filesAdapter, filesAdapter, a.Bus, a.Log); err != nil {
		a.Log.Warn("attach: upload service init failed", "err", err)
	} else {
		a.UploadSvc = up
	}
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
	if a.DBSizeMonitor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.DBSizeMonitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.Log.Warn("dbsize: run exited", "err", err)
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
