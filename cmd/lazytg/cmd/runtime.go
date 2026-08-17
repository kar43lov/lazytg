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

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"golang.org/x/term"

	"github.com/kar43lov/lazytg/internal/core/config"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
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

func (p *stdinPrompter) Code(_ context.Context, sent *tg.AuthSentCode) (string, error) {
	return p.readLine("Code from Telegram" + codeDelivery(sent) + codeFallback(sent) + ": ")
}

// codeFallback reports what Telegram says it would do next, which matters when
// the first channel delivers nothing: an app code is only useful if another
// session is online to receive it, and Telegram's own clients offer "resend as
// SMS" once Timeout elapses. Neither is visible from the prompt otherwise, so a
// user with no code has no idea whether waiting helps or what to wait for.
func codeFallback(sent *tg.AuthSentCode) string {
	if sent == nil {
		return ""
	}
	next, ok := sent.GetNextType()
	if !ok {
		return ""
	}
	var channel string
	switch next.(type) {
	case *tg.AuthCodeTypeSMS:
		channel = "SMS"
	case *tg.AuthCodeTypeCall:
		channel = "a voice call"
	case *tg.AuthCodeTypeFlashCall:
		channel = "a flash call"
	case *tg.AuthCodeTypeMissedCall:
		channel = "a missed call"
	case *tg.AuthCodeTypeFragmentSMS:
		channel = "Fragment"
	default:
		channel = next.TypeName()
	}
	if timeout, ok := sent.GetTimeout(); ok {
		return fmt.Sprintf(" [not arrived? resend switches to %s, allowed after %ds]", channel, timeout)
	}
	return fmt.Sprintf(" [not arrived? resend switches to %s]", channel)
}

// codeDelivery renders where Telegram says it put the code.
//
// The bare "Code from Telegram:" prompt costs real time on a first login: the
// code is routinely *not* an SMS but a service message inside an app that is
// already authorised, and it can equally be a missed call whose caller number
// is the code, an email, or a Fragment link. Telegram tells us which in the
// sentCode reply, and hiding that leaves the user hunting through devices while
// the code expires — every retry is another SendCode against an unofficial
// client, which is exactly the behavioural trace to avoid.
func codeDelivery(sent *tg.AuthSentCode) string {
	if sent == nil || sent.Type == nil {
		return ""
	}
	switch t := sent.Type.(type) {
	case *tg.AuthSentCodeTypeApp:
		return " (service message in the Telegram app on your other devices, not an SMS)"
	case *tg.AuthSentCodeTypeSMS:
		return " (SMS)"
	case *tg.AuthSentCodeTypeSMSWord:
		return " (SMS containing a single word)"
	case *tg.AuthSentCodeTypeSMSPhrase:
		return " (SMS containing a phrase)"
	case *tg.AuthSentCodeTypeCall:
		return " (voice call)"
	case *tg.AuthSentCodeTypeMissedCall:
		return fmt.Sprintf(" (missed call from %s… — the code is the last %d digits of the caller number)", t.Prefix, t.Length)
	case *tg.AuthSentCodeTypeFlashCall:
		return " (flash call — read the code off the caller number)"
	case *tg.AuthSentCodeTypeEmailCode:
		return fmt.Sprintf(" (email to %s)", t.EmailPattern)
	case *tg.AuthSentCodeTypeFragmentSMS:
		return fmt.Sprintf(" (Fragment — collect it at %s)", t.URL)
	case *tg.AuthSentCodeTypeFirebaseSMS:
		return " (SMS via Firebase)"
	case *tg.AuthSentCodeTypeSetUpEmailRequired:
		return " (Telegram wants a login email configured first — do that once in an official client)"
	default:
		// A type we do not know yet is still worth naming: it tells the user
		// what to search for instead of leaving them with a bare prompt.
		return fmt.Sprintf(" (%s)", sent.Type.TypeName())
	}
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

// newClient is a thin convenience that builds tg.Client from the resolved
// credentials (flags → env → embedded) and the supplied session store.
// Returns an error early if no layer supplied credentials, so subcommands
// short-circuit before opening a DB connection.
func newClient(store *tgclient.SessionStore) (*tgclient.Client, error) {
	return newClientWithUpdates(store, nil)
}

// newClientWithUpdates is newClient plus a live-update handler. gotd only
// accepts the handler when the client is constructed, so the TUI builds its
// dispatcher first and passes it in here; one-shot commands (login, logout)
// have no use for updates and go through newClient.
func newClientWithUpdates(store *tgclient.SessionStore, handler telegram.UpdateHandler) (*tgclient.Client, error) {
	apiID, apiHash, _, err := tgclient.ResolveCredentials(flagAPIID, flagAPIHash)
	if err != nil {
		return nil, err
	}
	return tgclient.New(tgclient.ClientConfig{
		APIID:         apiID,
		APIHash:       apiHash,
		SessionStore:  store,
		UpdateHandler: handler,
	})
}
