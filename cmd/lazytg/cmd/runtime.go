package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotd/td/tg"
	"golang.org/x/term"

	"github.com/pgmac/lazytg/internal/core/config"
	tgclient "github.com/pgmac/lazytg/internal/tg"
)

// dbFileName is the filename of the local SQLite database inside DataDir().
// Kept centralised so login/logout/accounts agree on where the DB lives.
const dbFileName = "lazytg.db"

// resolvePathsOnly returns the XDG paths without opening the SecretStore. Use
// this from read-only commands (accounts, version, debug-bundle) so they do
// not trigger a passphrase prompt on headless boxes that fall back to the
// age-encrypted file store.
func resolvePathsOnly() (config.Paths, error) {
	paths, err := config.Resolve()
	if err != nil {
		return config.Paths{}, fmt.Errorf("resolve paths: %w", err)
	}
	return paths, nil
}

// resolvePaths returns the XDG paths and an open SecretStore. The passphrase
// prompter is wired to read from /dev/tty (no echo) so it works inside SSH
// sessions where stdin may already be redirected. Only commands that touch
// session secrets (login, logout) should call this — others should use
// resolvePathsOnly to avoid a needless passphrase prompt.
func resolvePaths() (config.Paths, config.SecretStore, error) {
	paths, err := resolvePathsOnly()
	if err != nil {
		return config.Paths{}, nil, err
	}
	store, err := config.NewSecretStore(paths.Config, ttyPassphrasePrompter)
	if err != nil {
		return paths, nil, fmt.Errorf("open secret store: %w", err)
	}
	return paths, store, nil
}

// dbPath builds the absolute path to the SQLite file, given the resolved
// XDG data directory. Keeping it a function rather than a const lets multi-
// account setups override it later.
func dbPath(paths config.Paths) string {
	return filepath.Join(paths.Data, dbFileName)
}

// ttyPassphrasePrompter reads a passphrase from the controlling terminal
// without echoing it. Returns an error if no TTY is available — callers
// should explain how to set LAZYTG_API_ID/HASH in headless environments.
func ttyPassphrasePrompter() (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open /dev/tty: %w (set up a keyring or run interactively)", err)
	}
	defer func() { _ = tty.Close() }()

	if _, err := fmt.Fprint(tty, "Enter master passphrase for lazytg secrets: "); err != nil {
		return "", err
	}
	pw, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	// term.ReadPassword strips the terminating newline. Do NOT trim further:
	// passphrases may legitimately contain leading/trailing whitespace and
	// silently stripping them would make the secrets file undecryptable on
	// a future run.
	return string(pw), nil
}

// stdinPrompter is the CLI implementation of tg.CodePrompter. It reads phone
// and code from the user via the provided reader/writer (so tests can swap
// them) and reads the 2FA password without echo from /dev/tty.
//
// The bufio.Reader is built once and reused across prompts: bufio reads ahead,
// so re-creating it per prompt would discard buffered data and break piped
// input (`lazytg login < script.txt`) plus any multi-line buffer-based test.
type stdinPrompter struct {
	br        *bufio.Reader
	out       io.Writer
	presetPhn string
}

func newStdinPrompter(in io.Reader, out io.Writer, presetPhone string) *stdinPrompter {
	return &stdinPrompter{br: bufio.NewReader(in), out: out, presetPhn: presetPhone}
}

func (p *stdinPrompter) Phone(_ context.Context) (string, error) {
	if p.presetPhn != "" {
		return p.presetPhn, nil
	}
	return p.readLine("Phone (e.g. +79991112233): ")
}

func (p *stdinPrompter) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	return p.readLine("Code from Telegram: ")
}

func (p *stdinPrompter) Password(_ context.Context) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open /dev/tty for 2FA: %w", err)
	}
	defer func() { _ = tty.Close() }()

	if _, err := fmt.Fprint(tty, "2FA password: "); err != nil {
		return "", err
	}
	pw, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return "", fmt.Errorf("read 2FA password: %w", err)
	}
	// Do NOT trim: Telegram allows whitespace in 2FA passwords, and stripping
	// it would produce an opaque "wrong password" failure.
	return string(pw), nil
}

// readLine prints prompt and reads a single line; returns an error if the
// line is blank so the caller can decide whether to retry.
func (p *stdinPrompter) readLine(prompt string) (string, error) {
	if _, err := fmt.Fprint(p.out, prompt); err != nil {
		return "", err
	}
	line, err := p.br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("empty input")
	}
	return line, nil
}

// newClient is a thin convenience that builds tg.Client from env credentials
// and the supplied session store. Returns an error early if credentials are
// missing so subcommands can short-circuit before opening a DB connection.
func newClient(store *tgclient.SessionStore) (*tgclient.Client, error) {
	apiID, apiHash, err := tgclient.CredentialsFromEnv()
	if err != nil {
		return nil, err
	}
	return tgclient.New(tgclient.ClientConfig{
		APIID:        apiID,
		APIHash:      apiHash,
		SessionStore: store,
	})
}
