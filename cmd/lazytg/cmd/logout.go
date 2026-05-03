package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
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

	raw := strings.TrimSpace(flagAccount)
	if raw == "" {
		return errors.New("logout requires --account <phone>")
	}
	// Normalise so that callers who pass spaces or dashes hit the same
	// session-store key the matching `lazytg login` wrote.
	phone, err := domain.NormalizePhone(raw)
	if err != nil {
		return fmt.Errorf("phone %q: %w", raw, err)
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
