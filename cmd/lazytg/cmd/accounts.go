package cmd

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pgmac/lazytg/internal/storage/sqlite"
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

	paths, _, err := resolvePaths()
	if err != nil {
		return err
	}

	repo, err := sqlite.Open(ctx, dbPath(paths))
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

	active := strings.TrimSpace(flagAccount)
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
