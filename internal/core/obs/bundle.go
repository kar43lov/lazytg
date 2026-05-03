package obs

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// VersionInfo carries the build-stamped identification fields the bundle
// records in version.txt. Fields mirror what the `lazytg version`
// command prints; a dedicated type keeps the bundle producer free of
// tight coupling to the cobra command.
type VersionInfo struct {
	Version string
	Commit  string
	Date    string
}

// ConfigSource is the narrow contract Bundle needs from the config
// layer. It returns the path to the user's TOML configuration file (or
// "" when no file exists) so the bundle can copy a redacted snapshot
// in. The interface keeps obs free of a hard dependency on
// internal/core/config — the real implementation simply returns
// filepath.Join(paths.Config, "lazytg.toml").
type ConfigSource interface {
	ConfigPath() string
}

// BundleStore is the narrow storage surface the bundle producer needs:
// the SQLite handle for db_stats.txt counts and the on-disk path for
// file-size reporting. *sqlite.Repo satisfies it natively via DB() and
// DBPath(); the interface keeps obs from importing the storage package.
type BundleStore interface {
	DB() *sql.DB
	DBPath() string
}

// Bundle assembles a redacted diagnostics tarball. The contents are
// strictly minimal — only data the team actually triages from a bug
// report — and pass through Redact before serialisation.
//
// What the bundle deliberately does NOT include:
//   - session blobs (~/.config/lazytg/secrets.age, *.session)
//   - api_hash / api_id env values
//   - phone numbers, message text, chat titles, contact names
//   - the SQLite file itself (would be a copy of the user's entire
//     message history with no redaction layer to scrub it)
//
// Anything else the team needs to debug a specific issue should be
// requested in-band ("could you also attach a screenshot of the chat
// list" — explicit consent), not silently scooped into the bundle.
type Bundle struct {
	Logger  *slog.Logger
	Cfg     ConfigSource
	Version VersionInfo
	Store   BundleStore
	LogPath string

	// LogTailLines bounds the number of trailing lines copied from
	// LogPath into logs.txt. 0 picks the documented default
	// (DefaultBundleLogTail). The cap exists because lumberjack rotates
	// at 10 MB but operators often run for weeks between bundles —
	// without a tail the bundle would balloon past anything mailable.
	LogTailLines int
}

// DefaultBundleLogTail is the line cap applied when Bundle.LogTailLines
// is zero. 1000 lines × an upper-bound 1 KiB per JSON record stays well
// under 1 MiB even before tar.gz compression.
const DefaultBundleLogTail = 1000

// Create writes the diagnostic bundle to outPath as a tar.gz. Returns
// the absolute path of the file actually written (which equals outPath
// after filepath.Abs). On failure the partially-written file is
// removed so a re-run does not leave debris in the user's cwd.
func (b *Bundle) Create(ctx context.Context, outPath string) (string, error) {
	if outPath == "" {
		return "", errors.New("bundle: outPath is required")
	}
	abs, err := filepath.Abs(outPath)
	if err != nil {
		return "", fmt.Errorf("bundle: resolve out path: %w", err)
	}

	// G304: outPath comes from the operator (CLI flag or hard-coded
	// production default) — it is not user-supplied untrusted input.
	// We control the open mode (0600) and TRUNC semantics so a stale
	// .partial cannot leak.
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return "", fmt.Errorf("bundle: open %q: %w", abs, err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(abs)
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := b.writeAllEntries(ctx, tw); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return "", err
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return "", fmt.Errorf("bundle: tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("bundle: gzip close: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("bundle: fsync: %w", err)
	}

	cleanup = false
	if b.Logger != nil {
		b.Logger.Info("debug-bundle written", "path", abs)
	}
	return abs, nil
}

// writeAllEntries assembles the bundle file-by-file. Each writer
// produces an in-memory blob (the largest, logs.txt, is bounded by
// LogTailLines × ~1 KiB) so we can compute Tar headers with a
// known Size without two-pass writes. Errors short-circuit so a
// failure in one section aborts the whole bundle (better than
// shipping a half-populated archive that triages will misread).
func (b *Bundle) writeAllEntries(ctx context.Context, tw *tar.Writer) error {
	now := time.Now().UTC()

	type entry struct {
		name string
		body []byte
	}

	versionBody := []byte(b.formatVersion())

	configBody, err := b.readRedactedConfig()
	if err != nil {
		return fmt.Errorf("bundle: config: %w", err)
	}

	logsBody, err := b.readRedactedLogs()
	if err != nil {
		return fmt.Errorf("bundle: logs: %w", err)
	}

	dbBody, err := b.collectDBStats(ctx)
	if err != nil {
		return fmt.Errorf("bundle: db stats: %w", err)
	}

	goroutinesBody := []byte(Redact(captureGoroutines()))

	entries := []entry{
		{"version.txt", versionBody},
		{"config.toml", configBody},
		{"logs.txt", logsBody},
		{"db_stats.txt", dbBody},
		{"goroutines.txt", goroutinesBody},
	}

	for _, e := range entries {
		hdr := &tar.Header{
			Name:    e.name,
			Mode:    0o600,
			Size:    int64(len(e.body)),
			ModTime: now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("bundle: write header %q: %w", e.name, err)
		}
		if _, err := tw.Write(e.body); err != nil {
			return fmt.Errorf("bundle: write body %q: %w", e.name, err)
		}
	}
	return nil
}

// formatVersion renders the version.txt body — single-screen build
// metadata so triages can pin a bug to a specific binary.
func (b *Bundle) formatVersion() string {
	v := b.Version
	if v.Version == "" {
		v.Version = "dev"
	}
	if v.Commit == "" {
		v.Commit = "none"
	}
	if v.Date == "" {
		v.Date = "unknown"
	}
	return fmt.Sprintf(
		"lazytg %s\ncommit: %s\nbuilt:  %s\ngo:     %s/%s %s\n",
		v.Version, v.Commit, v.Date, runtime.GOOS, runtime.GOARCH, runtime.Version(),
	)
}

// readRedactedConfig returns the user's config file passed through
// Redact. A missing file is not an error — it just produces a
// placeholder body so triages can tell "no config" from "config
// missing in bundle" without scanning the file list.
func (b *Bundle) readRedactedConfig() ([]byte, error) {
	if b.Cfg == nil {
		return []byte("# no ConfigSource provided\n"), nil
	}
	path := b.Cfg.ConfigPath()
	if path == "" {
		return []byte("# no config file configured\n"), nil
	}
	// G304: path is supplied by ConfigSource (resolved from XDG dirs
	// at startup), not from user input. The bundle is opt-in via the
	// debug-bundle CLI command — no traversal vector exists.
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("# config file does not exist (default values in use)\n"), nil
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return []byte(Redact(string(raw))), nil
}

// readRedactedLogs copies the trailing LogTailLines from LogPath
// through Redact. Lumberjack rotates the file at 10 MB so reading from
// the front would surface stale records ahead of recent ones; we want
// the tail because that is where the bug-of-the-day lives. Missing
// file degrades gracefully: triages can still send the bundle without
// the logs, paired with a note about the rotation policy.
func (b *Bundle) readRedactedLogs() ([]byte, error) {
	if b.LogPath == "" {
		return []byte("# no log file configured\n"), nil
	}
	limit := b.LogTailLines
	if limit <= 0 {
		limit = DefaultBundleLogTail
	}

	// G304: LogPath is supplied at wire-time from XDG StateDir, not
	// from user input.
	f, err := os.Open(b.LogPath) //nolint:gosec // operator-supplied log path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("# log file does not exist yet\n"), nil
		}
		return nil, fmt.Errorf("open log %q: %w", b.LogPath, err)
	}
	defer func() { _ = f.Close() }()

	// Ring-buffer keeps memory bounded to limit lines regardless of
	// file size. Buffer is bumped to 16 MiB so a single pathologically
	// long log line still fits; if a line still exceeds that the bundle
	// degrades gracefully by replacing the offender with a placeholder
	// rather than aborting the whole read — debug-bundle is the
	// triager's last-resort tool and must not fail on malformed input.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	ring := make([]string, 0, limit)
	appendLine := func(line string) {
		if len(ring) < limit {
			ring = append(ring, line)
			return
		}
		copy(ring, ring[1:])
		ring[limit-1] = line
	}
	for sc.Scan() {
		appendLine(sc.Text())
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			appendLine("# truncated: log line exceeded 16 MiB scanner buffer")
		} else {
			return nil, fmt.Errorf("scan log %q: %w", b.LogPath, err)
		}
	}

	var sb strings.Builder
	for _, line := range ring {
		sb.WriteString(Redact(line))
		sb.WriteByte('\n')
	}
	return []byte(sb.String()), nil
}

// collectDBStats writes a small text report with row counts and
// schema state. Counts are read with COUNT(*) which is fine for our
// scale (the FTS5 index is the largest single table by far and its
// row count is bounded by lazy-index defaults). No message text is
// included — only counts.
func (b *Bundle) collectDBStats(ctx context.Context) ([]byte, error) {
	if b.Store == nil {
		return []byte("# no BundleStore provided\n"), nil
	}

	var sb strings.Builder
	if path := b.Store.DBPath(); path != "" {
		// Replace the user's home prefix with `~` so the bundle does
		// not leak the OS username via the absolute db path. The bundle
		// is meant for sharing in bug reports — the path itself is not
		// a Telegram secret but the username is unrelated PII the
		// triager does not need.
		fmt.Fprintf(&sb, "db_path: %s\n", redactHomePrefix(path))
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&sb, "db_size_bytes: %d\n", info.Size())
		} else if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(&sb, "db_size_bytes: 0 (file missing)")
		} else {
			fmt.Fprintf(&sb, "db_size_bytes: <stat error: %v>\n", err)
		}
	} else {
		fmt.Fprintln(&sb, "db_path: <in-memory or file: URI>")
	}

	db := b.Store.DB()
	if db == nil {
		fmt.Fprintln(&sb, "# DB() returned nil — no further stats available")
		return []byte(sb.String()), nil
	}

	// schema_version: max(applied) — works on any DB the migrations
	// runner has ever touched, including an empty schema_migrations.
	var schemaVer sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&schemaVer); err != nil {
		fmt.Fprintf(&sb, "schema_version: <query error: %v>\n", err)
	} else {
		fmt.Fprintf(&sb, "schema_version: %d\n", schemaVer.Int64)
	}

	// Pre-defined per-table counts. We list them rather than
	// SELECTing sqlite_master so the bundle never accidentally counts
	// a table that holds user-tagged content (e.g. a future
	// `bookmarks` table) without an explicit decision here.
	tables := []string{"accounts", "chats", "messages", "messages_fts", "peers", "outgoing", "downloaded_files"}
	for _, t := range tables {
		var count int64
		err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, t)).Scan(&count)
		if err != nil {
			// Tables added in later migrations may not exist on
			// older databases; surface the absence without aborting.
			fmt.Fprintf(&sb, "%s_count: <unavailable: %v>\n", t, err)
			continue
		}
		fmt.Fprintf(&sb, "%s_count: %d\n", t, count)
	}
	return []byte(sb.String()), nil
}

// redactHomePrefix replaces the leading user-home directory with the
// literal `~` so on-disk paths in the bundle do not leak the OS
// username. The substitution is purely textual: empty home or paths
// outside it pass through unchanged.
func redactHomePrefix(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// captureGoroutines returns the runtime goroutine dump. It is large
// (~10–50 KiB on a typical session) but compresses very well in the
// gzip layer and is the single highest-signal piece of data for
// diagnosing deadlocks. The dump never contains user data — only Go
// stack frames and goroutine ids.
//
// Buffer grows until runtime.Stack reports a non-truncating fill so a
// busy app with thousands of goroutines does not silently lose frames
// past a fixed ceiling — the bundle's primary use case is exactly the
// pathological "what is everyone waiting on" scenario.
func captureGoroutines() string {
	const captureGoroutinesCeiling = 64 << 20 // safety bound: 64 MiB
	buf := make([]byte, 64<<10)               // 64 KiB initial
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		if len(buf) >= captureGoroutinesCeiling {
			return string(buf[:n]) + "\n# TRUNCATED at 64 MiB — too many goroutines\n"
		}
		buf = make([]byte, len(buf)*2)
	}
}
