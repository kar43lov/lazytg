package obs_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/obs"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
)

// stubConfig satisfies obs.ConfigSource by returning a fixed path.
// We pass it into Bundle so the grep test can drop a config containing
// known sensitive values into the right place and assert they get
// scrubbed before serialisation.
type stubConfig struct{ path string }

func (s stubConfig) ConfigPath() string { return s.path }

// TestBundle_GrepNoSecrets is the security-defining test for the
// debug-bundle pipeline. The plan calls it out explicitly: if the
// bundle ever leaks an api_hash, MTProto session, phone number or
// raw message text, this test must catch it.
//
// We seed a repo with three messages whose text contains each kind of
// secret (api_hash hex, base64 session blob, +country phone), drop a
// real config file with an api_hash assignment and a "session" file
// with base64 content next to where the bundle would normally pull
// from, and then assert that none of those literal byte sequences
// appear inside any of the bundle's tar entries.
func TestBundle_GrepNoSecrets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Repo: three messages whose text mirrors what unredacted logs
	// would look like.
	repo, ctx := openSeededRepo(t, dir)

	// Config: TOML-ish file with an api_hash setting. The Redact
	// helper scrubs the hex value but keeps the surrounding key so
	// the bundle still tells the triager "yes, an api_hash was set".
	cfgPath := filepath.Join(dir, "lazytg.toml")
	cfgBody := `# lazytg config
api_id = 12345
api_hash = "abc123def456789012345678901234567890aabb"
default_account = "+79991234567"
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Logs: one real-looking JSON line per secret kind so the line
	// count tail behaviour is exercised.
	logPath := filepath.Join(dir, "lazytg.log")
	logBody := strings.Join([]string{
		`{"level":"info","msg":"login","phone":"+79991234567"}`,
		`{"level":"debug","msg":"auth","api_hash":"abc123def456789012345678901234567890aabb"}`,
		`{"level":"info","msg":"session restore","blob":"AaBb/0123456789abcdef0123456789abcdef/+++ZzYyXxWwVvUuTtSsRr"}`,
		`{"level":"info","msg":"app started"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(logBody), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	bundle := &obs.Bundle{
		Cfg: stubConfig{path: cfgPath},
		Version: obs.VersionInfo{
			Version: "v0.1.0-test",
			Commit:  "deadbeef",
			Date:    "2026-05-03",
		},
		Store:        repo,
		LogPath:      logPath,
		LogTailLines: 100,
	}

	outPath := filepath.Join(dir, "bundle.tar.gz")
	got, err := bundle.Create(ctx, outPath)
	if err != nil {
		t.Fatalf("bundle.Create: %v", err)
	}
	if got != outPath {
		t.Fatalf("returned path mismatch: got %q, want %q", got, outPath)
	}

	// Hard-coded list of byte sequences that must never appear
	// anywhere in the bundle. The api_hash hex / session base64 /
	// phone number are tested verbatim; the message-text triplet
	// guards the "we silently included raw chat history" failure
	// mode (which would also be a privacy regression even if no
	// "secret" pattern appears).
	forbidden := []string{
		`abc123def456789012345678901234567890aabb`,                    // api_hash hex
		`AaBb/0123456789abcdef0123456789abcdef/+++ZzYyXxWwVvUuTtSsRr`, // session blob
		`+79991234567`,         // phone
		"secret api_hash test", // message text
		"session content",      // message text
	}

	entries := readTarGz(t, outPath)
	if len(entries) == 0 {
		t.Fatalf("bundle is empty — expected version.txt + config.toml + logs.txt + db_stats.txt + goroutines.txt")
	}

	for name, body := range entries {
		for _, needle := range forbidden {
			if bytes.Contains(body, []byte(needle)) {
				// Surface a small window so debugging is fast — printing the
				// whole 50-100 KiB goroutine dump would drown the failure.
				idx := bytes.Index(body, []byte(needle))
				start := idx - 40
				if start < 0 {
					start = 0
				}
				end := idx + len(needle) + 40
				if end > len(body) {
					end = len(body)
				}
				t.Fatalf("LEAK in %s: forbidden substring %q found at offset %d\nwindow: %q",
					name, needle, idx, body[start:end])
			}
		}
	}

	// Sanity: the bundle MUST contain something useful — otherwise an
	// empty tarball would trivially pass the leak check.
	for _, want := range []string{"version.txt", "config.toml", "logs.txt", "db_stats.txt", "goroutines.txt"} {
		if _, ok := entries[want]; !ok {
			t.Errorf("bundle missing expected entry %q (have %v)", want, sortedKeys(entries))
		}
	}

	// Stat must show a 0600 file — no group/other can read a bundle
	// that holds redacted-but-still-private operator state.
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("bundle file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestBundle_MissingPaths_DegradesGracefully(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, ctx := openSeededRepo(t, dir)

	bundle := &obs.Bundle{
		Cfg:     stubConfig{path: filepath.Join(dir, "no-such-config.toml")},
		Store:   repo,
		LogPath: filepath.Join(dir, "no-such-log.log"),
	}

	outPath := filepath.Join(dir, "bundle.tar.gz")
	if _, err := bundle.Create(ctx, outPath); err != nil {
		t.Fatalf("Create with missing inputs should succeed (degrades to placeholders): %v", err)
	}

	entries := readTarGz(t, outPath)
	if !bytes.Contains(entries["config.toml"], []byte("does not exist")) {
		t.Errorf("config placeholder missing: %q", entries["config.toml"])
	}
	if !bytes.Contains(entries["logs.txt"], []byte("does not exist")) {
		t.Errorf("log placeholder missing: %q", entries["logs.txt"])
	}
}

func TestBundle_LogTailLimitsLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, ctx := openSeededRepo(t, dir)

	logPath := filepath.Join(dir, "lazytg.log")
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		sb.WriteString(`{"msg":"line `)
		sb.WriteString(itoa(i))
		sb.WriteString(`"}`)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	bundle := &obs.Bundle{
		Store:        repo,
		LogPath:      logPath,
		LogTailLines: 50,
	}
	outPath := filepath.Join(dir, "bundle.tar.gz")
	if _, err := bundle.Create(ctx, outPath); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries := readTarGz(t, outPath)
	logs := entries["logs.txt"]
	count := bytes.Count(logs, []byte("\n"))
	if count > 50 {
		t.Errorf("log tail not bounded: got %d lines, want ≤ 50", count)
	}
	// Must contain the *last* line; tail semantics matter.
	if !bytes.Contains(logs, []byte(`"line 4999"`)) {
		t.Errorf("expected tail to include final line, got %q", logs[max(0, len(logs)-200):])
	}
}

func TestBundle_RemovesPartialOnFailure(t *testing.T) {
	t.Parallel()

	// Pointing Bundle at a directory that does not exist forces a
	// failure during open. After the error we must not leave a
	// partially-written file behind in the parent dir.
	dir := t.TempDir()
	bundle := &obs.Bundle{}
	out := filepath.Join(dir, "subdir-does-not-exist", "bundle.tar.gz")
	if _, err := bundle.Create(context.Background(), out); err == nil {
		t.Fatal("expected error when output dir is missing")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("partial file should not remain on error: stat err = %v", err)
	}
}

// openSeededRepo opens a fresh repo at <dir>/lazytg.db and inserts
// three rows whose text contains each kind of "must-not-leak" content
// the bundle test guards against. Returned ctx is bound to the test
// timeout via t.Context().
func openSeededRepo(t *testing.T, dir string) (*sqlite.Repo, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	repo, err := sqlite.Open(ctx, filepath.Join(dir, "lazytg.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	chat := domain.Chat{
		ID:              42,
		Type:            domain.ChatTypePrivate,
		Title:           "Alice",
		LastMessageDate: time.Unix(1000, 0).UTC(),
	}
	if err := repo.SaveChat(ctx, chat); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	msgs := []domain.Message{
		{ChatID: 42, ID: 1, Date: now, Text: "secret api_hash test"},
		{ChatID: 42, ID: 2, Date: now, Text: "session content"},
		{ChatID: 42, ID: 3, Date: now, Text: "+79991234567"},
	}
	if err := repo.SaveMessages(ctx, msgs); err != nil {
		t.Fatalf("save messages: %v", err)
	}
	return repo, ctx
}

// readTarGz extracts the bundle into a name → body map. Entries
// outside the top-level (we never write any) would still be loaded
// here so a regression that adds a "session/secret.bin" path would
// surface in the diff.
func readTarGz(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gz.Close() }()

	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read body %q: %v", hdr.Name, err)
		}
		out[hdr.Name] = body
	}
	return out
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Cheap deterministic order — bubble sort is fine for ≤ 10 entries.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// itoa is a small stack-allocated formatter used by the log-tail
// limit test. Avoids the strconv dependency for what is otherwise
// trivial code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
