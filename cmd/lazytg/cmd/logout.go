package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pgmac/lazytg/internal/storage/sqlite"
	tgclient "github.com/pgmac/lazytg/internal/tg"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget a Telegram account",
		Long: "Removes the stored session for --account from the secret store and\n" +
			"deletes the matching row from the local database. Local message data\n" +
			"is left intact so re-login does not re-download the entire history.\n",
		RunE: runLogout,
	}
}

func runLogout(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	phone := strings.TrimSpace(flagAccount)
	if phone == "" {
		return errors.New("logout requires --account <phone>")
	}

	paths, secrets, err := resolvePaths()
	if err != nil {
		return err
	}

	sess := tgclient.NewSessionStore(secrets, phone)
	if err := sess.Forget(); err != nil {
		return fmt.Errorf("forget session: %w", err)
	}

	repo, err := sqlite.Open(ctx, dbPath(paths))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	if err := repo.DeleteAccount(ctx, phone); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "logged out %s\n", phone); err != nil {
		return err
	}
	return nil
}
