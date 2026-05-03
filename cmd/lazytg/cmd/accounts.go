package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func newAccountsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "List logged-in Telegram accounts",
		Long: "Prints the accounts known to the local database, marking the one that\n" +
			"matches --account (if any) as active. Read-only — no Telegram API call.\n",
		RunE: runAccounts,
	}
}

func runAccounts(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	paths, err := resolvePathsOnly()
	if err != nil {
		return err
	}

	// `accounts` is documented as read-only. If the user has never run
	// `lazytg login`, the SQLite file does not exist — opening it here would
	// silently materialise the database (and run migrations) just to print
	// "no accounts logged in", contradicting the read-only contract. Stat
	// first and short-circuit when the file is missing.
	dbFile := dbPath(paths)
	if _, statErr := os.Stat(dbFile); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "no accounts logged in (run `lazytg login --account +<phone>`)")
			return err
		}
		return fmt.Errorf("stat database: %w", statErr)
	}

	repo, err := sqlite.Open(ctx, dbFile)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	accounts, err := repo.GetAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	if len(accounts) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no accounts logged in (run `lazytg login --account +<phone>`)")
		return err
	}

	// Stored phones are canonical (NormalizePhone form), so normalise --account
	// before the comparison — otherwise `--account "+7 999 111 22 33"` and the
	// stored "+79991112233" would never match and the active marker would
	// silently disappear. Fall back to the trimmed value if normalisation
	// fails so the comparison still happens (no row will match an invalid
	// phone, which is the desired outcome).
	active := strings.TrimSpace(flagAccount)
	if normalised, err := domain.NormalizePhone(active); err == nil {
		active = normalised
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTIVE\tPHONE\tALIAS\tCREATED"); err != nil {
		return err
	}
	for _, a := range accounts {
		marker := ""
		if a.Phone == active {
			marker = "*"
		}
		alias := a.Alias
		if alias == "" {
			alias = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			marker, a.Phone, alias, a.CreatedAt.Format("2006-01-02 15:04")); err != nil {
			return err
		}
	}
	return tw.Flush()
}
