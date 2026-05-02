package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
	tgclient "github.com/pgmac/lazytg/internal/tg"
)

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate a Telegram account",
		Long: "Logs in to Telegram via phone+code+2FA and persists the session\n" +
			"in the OS keyring (or an age-encrypted file fallback). On success the\n" +
			"account is recorded in the local database so 'lazytg accounts' can\n" +
			"list it without a network round-trip.\n",
		RunE: runLogin,
	}
}

func runLogin(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	paths, secrets, err := resolvePaths()
	if err != nil {
		return err
	}

	prompter := newStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout(), strings.TrimSpace(flagAccount))

	// We need the phone before opening the session store because the store is
	// keyed on phone. If the user passed --account we use that; otherwise we
	// ask via the prompter and reuse the answer below — the auth flow takes
	// `phone` directly so the prompter's Phone() is not called again.
	phone := strings.TrimSpace(flagAccount)
	if phone == "" {
		phone, err = prompter.Phone(ctx)
		if err != nil {
			return fmt.Errorf("read phone: %w", err)
		}
	}

	sess := tgclient.NewSessionStore(secrets, phone)
	tgc, err := newClient(sess)
	if err != nil {
		return err
	}

	// Open the DB before running auth. The session is persisted to the
	// SecretStore *during* auth (gotd writes via SessionStore.Set), so a DB
	// failure that happens after a successful auth leaves the user in an
	// inconsistent state where the session exists but `lazytg accounts`
	// shows nothing. Failing fast on DB-open keeps the two stores in sync.
	repo, err := sqlite.Open(ctx, dbPath(paths))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	if err := tgc.Run(ctx, func(ctx context.Context) error {
		if err := tgclient.Login(ctx, tgc.Raw(), phone, prompter); err != nil {
			return fmt.Errorf("login: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := repo.SaveAccount(ctx, domain.Account{Phone: phone}); err != nil {
		return fmt.Errorf("save account: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "logged in as %s\n", phone); err != nil {
		return err
	}
	return nil
}
