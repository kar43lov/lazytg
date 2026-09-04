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
	"sort"
	"sync"

	"github.com/gotd/td/telegram/updates"

	"github.com/kar43lov/lazytg/internal/core/config"
	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/files"
	"github.com/kar43lov/lazytg/internal/core/obs"
	"github.com/kar43lov/lazytg/internal/core/search"
	"github.com/kar43lov/lazytg/internal/core/security"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/palette"
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
	Client    *tgclient.Client
	Sender    *coresync.SendService
	History   *coresync.HistoryService
	Backfill  *coresync.BackfillService
	Updates   *tgclient.UpdatesDispatcher
	Reconnect *coresync.ReconnectManager
	// Read acknowledges opened chats to Telegram. Nil until a session is
	// attached, so a cache-only launch simply never marks anything read.
	Read *coresync.ReadService
	// Folders reads the account's chat folders. Nil until a session is
	// attached; the chat list then simply has no tabs, which is what an
	// account without folders looks like anyway.
	Folders *tgclient.FoldersFetcher

	// Actions edits and deletes messages that already exist. Nil until a
	// session is attached: both are round trips, and a cache-only launch
	// has nothing to send them to.
	Actions *coresync.ActionService
	// Rediscover refreshes the chat list when the live path invents a chat,
	// which is the only way such a chat ever gets a title. Built by the cmd
	// layer, which is the first place Dialogs exists.
	Rediscover  *coresync.Rediscoverer
	DownloadSvc *files.DownloadService
	UploadSvc   *files.UploadService

	// Dialogs fills the chats table from Telegram. Nothing else populates
	// it, so until this runs the TUI has an empty chat list no matter how
	// healthy the connection is.
	Dialogs *coresync.DialogsService

	// HistoryFetcher is the raw MTProto history provider, handed to the
	// thread pane so it can pull older messages when the local cache is
	// thinner than the viewport.
	HistoryFetcher *tgclient.HistoryFetcher

	// Polling mirrors the --polling flag: it makes AttachClient build
	// PollingSvc below. It is not a replacement for the live dispatcher but
	// a net under it — see PollingSvc.
	Polling bool

	// PollingSvc periodically re-reads the most recently active chats and
	// publishes anything the live path did not deliver. Non-nil only when
	// Polling is set, because it costs one messages.getHistory per polled
	// chat per tick forever, and lazytg's whole update story is otherwise
	// push-based. Started by the cmd layer alongside the session.
	PollingSvc *tgclient.PollingFallback

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

// CheckPermissions runs the canonical Stage 3 audit set
// (secrets.age, ConfigDir, ConfigDir/.../lazytg.db, StateDir) outside
// of the full Build. CLI subcommands that open the SQLite repo
// directly (lazytg reindex, lazytg debug-bundle) call this so the
// security promise — fail-fast on tampered modes — is consistent
// across entry points.
func CheckPermissions(paths config.Paths, log *slog.Logger) error {
	return runStartupPermissionsAudit(paths, log)
}

// runStartupPermissionsAudit runs the canonical Stage 3 audit set
// (secrets.age, ConfigDir, DataDir, DataDir/lazytg.db, StateDir) and
// either returns a wrapped error (fail-class findings — boot must
// abort) or logs a single warn line per warn-class finding. Pulled
// out of Build so the audit list lives in one place and tests can
// call it directly.
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
			Path:         paths.Data,
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

// AttachOption customises AttachClient. Options exist so the cmd layer can
// hand in the one thing only it owns — the session supervisor — without
// AttachClient growing a parameter that every test then has to pass nil for.
type AttachOption func(*attachOptions)

type attachOptions struct {
	reconnect func(context.Context) error
}

// WithReconnector supplies the function ReconnectManager calls to stand a
// dead session back up. Without it the manager can still observe a
// disconnect and report it, but it cannot repair one: the MTProto run loop
// belongs to the cmd layer, and only the cmd layer can start another.
func WithReconnector(fn func(context.Context) error) AttachOption {
	return func(o *attachOptions) { o.reconnect = fn }
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
func (a *App) AttachClient(bgCtx context.Context, client *tgclient.Client, opts ...AttachOption) {
	if client == nil {
		return
	}
	a.Client = client

	var attachOpts attachOptions
	for _, opt := range opts {
		opt(&attachOpts)
	}

	historyFetcher := tgclient.NewHistoryFetcher(client.API())
	a.HistoryFetcher = historyFetcher

	a.History = coresync.NewHistoryService(historyFetcher, peerLookupAdapter{peers: a.Peers}, a.Repo, a.Bus, a.Log)
	a.Read = coresync.NewReadService(
		tgclient.NewReader(client.API(), peerResolverAdapter{peers: a.Peers}),
		a.Repo, a.Bus, a.Log)
	// Log rather than swallow: a nil Backfill means chats open with whatever
	// history is already cached and never fetch more, which is indistinguishable
	// from a network problem unless the construction failure is stated.
	if backfill, err := coresync.NewBackfillService(a.History, a.Log, coresync.BackfillConfig{}); err != nil {
		a.Log.Warn("attach: backfill service init failed — chats will not fetch history", "err", err)
	} else {
		a.Backfill = backfill
	}

	// The chat list has to come from somewhere before any of the services
	// below have a chat to act on. A construction failure is logged rather
	// than fatal: the rest of the attach still yields a usable session over
	// whatever is already cached locally.
	if dialogs, err := coresync.NewDialogsService(
		tgclient.NewDialogsFetcher(client.API()),
		a.Repo,
		a.Peers,
		a.Bus,
		a.Log,
		coresync.DialogsConfig{},
	); err != nil {
		a.Log.Warn("attach: dialogs service init failed", "err", err)
	} else {
		a.Dialogs = dialogs
	}

	a.Folders = tgclient.NewFoldersFetcher(client.API())

	// The edit path refuses somebody else's message without a round trip by
	// reading the direction migration 0010 stores, so no self-id lookup is
	// plumbed through here.
	a.Actions = coresync.NewActionService(
		tgclient.NewEditor(client.API(), peerResolverAdapter{peers: a.Peers}),
		tgclient.NewDeleter(client.API(), peerResolverAdapter{peers: a.Peers}),
		tgclient.NewForwarder(client.API(), peerResolverAdapter{peers: a.Peers}),
		a.Repo, a.Bus, a.Log)

	sender := tgclient.NewSender(client.API(), peerResolverAdapter{peers: a.Peers})
	a.Sender = coresync.NewSendService(senderAdapter{sender: sender}, outgoingStoreAdapter{repo: a.Repo}, a.Bus, a.Log, coresync.SendConfig{}).
		WithBackgroundContext(bgCtx).
		WithRateLimiter(a.SendGuard)

	// Preserve a dispatcher installed before attach. The cmd layer has to
	// build one ahead of the client (gotd takes the update handler at
	// construction time only) and hands it in through App.Updates;
	// overwriting it here would leave the live-update path pointing at a
	// dispatcher nobody is feeding.
	if a.Updates == nil {
		a.Updates = tgclient.NewUpdatesDispatcher(a.Bus, a.Log)
	}

	// The polling fallback is opt-in and stays off by default: it is steady
	// background traffic on an account Telegram already watches more closely
	// for being an unofficial client. Built after the dispatcher because it
	// publishes through the dispatcher's duplicate filter — the two paths
	// overlap by design, and one filter across both is what keeps a message
	// that arrives on each from being shown twice.
	if a.Polling {
		a.PollingSvc = tgclient.NewPollingFallback(
			pollingSourceAdapter{repo: a.Repo, peers: a.Peers},
			tgclient.NewPollingFetcher(historyFetcher),
			a.Bus, a.Log, 0).
			WithDeduper(a.Updates)
	}
	a.Reconnect = coresync.NewReconnectManager(
		reconnectAdapter{client: client, restart: attachOpts.reconnect},
		a.Bus, a.Log, coresync.ReconnectConfig{})

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
		// Reuse the same SendGuard the text-send path consults so file
		// uploads count toward the 10 msg/sec ban-risk ceiling. CLAUDE.md
		// promises the guard "не отключается" — without this the upload
		// path would silently bypass it.
		a.UploadSvc = up.WithRateLimiter(a.SendGuard)
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

	// Plumb the app-scoped ctx into SearchSvc so the lazy reindex
	// trigger uses it instead of inheriting the per-query 5 s overlay
	// timeout. Doing this here (rather than in Build) keeps the search
	// service decoupled from the cmd-layer ctx until the background
	// goroutines actually start.
	if a.SearchSvc != nil {
		a.SearchSvc.WithBackgroundContext(ctx)
	}

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
			// The detector loops until the context ends, so it never
			// returns a nil error — staticcheck proves it, and the
			// discarded `err != nil` guard read as if it might. What is
			// worth logging is an exit for any other reason.
			if err := a.Degradation.Run(ctx); !errors.Is(err, context.Canceled) {
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

// UpdatesManager builds the gap-recovering update handler for dispatcher,
// backed by this App's SQLite state and peer tables. The cmd layer installs
// the result as the client's update handler and then calls
// Client.RunGapRecovery once the session is authorised.
//
// Built here rather than in the cmd layer because it needs two storage
// handles the App already owns, and because "which storage backs the update
// state" is a composition decision, not a command-line one.
func (a *App) UpdatesManager(dispatcher *tgclient.UpdatesDispatcher) *updates.Manager {
	if dispatcher == nil || a.StateRepo == nil {
		return nil
	}
	return dispatcher.Manager(a.StateRepo, channelAccessHasher{peers: a.Peers})
}

// channelAccessHasher lets updates.Manager reuse the peer table lazytg
// already keeps instead of the in-memory default. It matters across
// restarts: the manager loads a channel's stored pts only when it can also
// find that channel's access hash, so with the in-memory hasher every
// channel is skipped on the next start and gap recovery covers only private
// chats and basic groups.
//
// userID is ignored on purpose. Accounts are separated by data directory
// (the --account flag picks one), so a database only ever holds one
// account's peers, and threading an id through would encode a scoping rule
// the storage layer does not have.
type channelAccessHasher struct {
	peers *sqlite.PeerRepo
}

func (h channelAccessHasher) SetChannelAccessHash(ctx context.Context, _, channelID, accessHash int64) error {
	if h.peers == nil {
		return errors.New("updates: no peer store")
	}
	// Preserve the recorded kind when the row already exists. Channels and
	// supergroups both address as InputPeerChannel, so overwriting one with
	// the other would not break sending — it would just quietly make the
	// stored type wrong for everything else that reads it.
	peerType := domain.ChatTypeChannel
	if existing, err := h.peers.Get(ctx, channelID); err == nil && existing.Type != "" {
		peerType = existing.Type
	}
	return h.peers.Save(ctx, domain.Peer{ID: channelID, Type: peerType, AccessHash: accessHash})
}

func (h channelAccessHasher) GetChannelAccessHash(ctx context.Context, _, channelID int64) (int64, bool, error) {
	if h.peers == nil {
		return 0, false, nil
	}
	peer, err := h.peers.Get(ctx, channelID)
	if err != nil {
		if errors.Is(err, sqlite.ErrPeerNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return peer.AccessHash, true, nil
}

// pollingActiveChats caps how many conversations one poll pulls. Three at
// the default three-second cadence is one request per second — well under
// the send guard's ceiling, and close to the traffic a person reading their
// most recent conversations would generate anyway. Raising it is a ban-risk
// decision, not a throughput one.
const pollingActiveChats = 3

// pollingSourceAdapter answers the fallback's only question — which chats to
// pull and from which message id — out of the local mirror, so the polling
// loop needs no connection to the UI and no state of its own.
//
// Ordering is by last activity alone, deliberately ignoring the pinned-first
// order GetChats returns: pinning says where a chat sits in the list, not
// where messages are arriving, and a dormant pinned chat would otherwise
// occupy one of the three slots permanently.
type pollingSourceAdapter struct {
	repo  *sqlite.Repo
	peers *sqlite.PeerRepo
}

func (a pollingSourceAdapter) Active(ctx context.Context) ([]tgclient.PolledChat, error) {
	chats, err := a.repo.GetChats(ctx)
	if err != nil {
		return nil, fmt.Errorf("polling: read chats: %w", err)
	}
	sort.SliceStable(chats, func(i, j int) bool {
		return chats[i].LastMessageDate.After(chats[j].LastMessageDate)
	})

	lookup := peerLookupAdapter{peers: a.peers}
	out := make([]tgclient.PolledChat, 0, pollingActiveChats)
	for _, c := range chats {
		if len(out) == pollingActiveChats {
			break
		}
		peer, err := lookup.Lookup(ctx, c.ID)
		if err != nil {
			// No access hash cached: the chat cannot be addressed over
			// MTProto at all. Skipped silently on purpose — logging here
			// would produce a line every three seconds for as long as the
			// dialog sync has not caught up.
			continue
		}
		out = append(out, tgclient.PolledChat{
			ChatID:     c.ID,
			AccessHash: peer.AccessHash,
			Type:       peer.Type,
			LastSeenID: a.newestStored(ctx, c.ID),
		})
	}
	return out, nil
}

// newestStored is the id of the newest message already in the mirror, which
// is what keeps the fallback from re-publishing whatever the live dispatcher
// has already delivered. A read failure yields 0 rather than an error: the
// worst case is one duplicate publish, and the persistence path upserts.
func (a pollingSourceAdapter) newestStored(ctx context.Context, chatID int64) int64 {
	msgs, err := a.repo.GetMessages(ctx, chatID, 1, 0)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	return msgs[0].ID
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

// reconnectAdapter bridges *tg.Client into coresync.ReconnectClient.
//
// The MTProto run loop is owned by the cmd layer, because it needs the TUI's
// context and its lifetime is the TUI's lifetime. Connect therefore delegates
// to the restart function that layer supplies (cmd's sessionSupervisor.
// Restart), rather than pretending to reconnect on its own: for one release
// this method returned nil immediately, which told ReconnectManager that a
// dead session had been repaired and left the client bound to a connection
// that could no longer send.
//
// The adapter also forwards the client's transport-level state feed, so the
// manager can report gotd's own in-session reconnects without owning them.
type reconnectAdapter struct {
	client  *tgclient.Client
	restart func(context.Context) error
}

func (a reconnectAdapter) Connect(ctx context.Context) error {
	if a.restart == nil {
		// Reported rather than swallowed: a manager that believes it
		// reconnected publishes "online" over a session that is gone, which
		// is the one outcome worse than staying offline.
		return errors.New("reconnect: no session supervisor wired (pass app.WithReconnector)")
	}
	return a.restart(ctx)
}

// ConnectionStates satisfies coresync.ConnectionStateReporter.
func (a reconnectAdapter) ConnectionStates() <-chan string {
	if a.client == nil {
		return nil
	}
	return a.client.ConnectionStates()
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
