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
	// ask via the prompter and reuse the answer below.
	phone := strings.TrimSpace(flagAccount)
	if phone == "" {
		phone, err = prompter.Phone(ctx)
		if err != nil {
			return fmt.Errorf("read phone: %w", err)
		}
		// Once we know the phone, the prompter must not ask again inside the
		// auth flow — pass it to the adapter via Login's phone argument.
		prompter.presetPhn = phone
	}

	sess := tgclient.NewSessionStore(secrets, phone)
	tgc, err := newClient(sess)
	if err != nil {
		return err
	}

	if err := tgc.Run(ctx, func(ctx context.Context) error {
		if err := tgclient.Login(ctx, tgc.Raw(), phone, prompter); err != nil {
			return fmt.Errorf("login: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	repo, err := sqlite.Open(ctx, dbPath(paths))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	if err := repo.SaveAccount(ctx, domain.Account{Phone: phone}); err != nil {
		return fmt.Errorf("save account: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "logged in as %s\n", phone); err != nil {
		return err
	}
	return nil
}
