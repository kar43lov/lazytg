package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
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

	// Phone is the secret-store key and the accounts.phone primary key, so it
	// must be canonicalised before either store is touched. Without this,
	// `+7 999 111 22 33` and `+79991112233` would create two distinct sessions
	// and accounts rows for the same Telegram account.
	rawPhone := strings.TrimSpace(flagAccount)
	prompter := newStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout(), rawPhone)

	if rawPhone == "" {
		rawPhone, err = prompter.Phone(ctx)
		if err != nil {
			return fmt.Errorf("read phone: %w", err)
		}
	}
	phone, err := domain.NormalizePhone(rawPhone)
	if err != nil {
		return fmt.Errorf("phone %q: %w", rawPhone, err)
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
	// shows nothing. Failing fast on DB-open catches the common failure
	// (DB unwritable) before we burn an SMS code; SaveAccount errors after
	// auth are handled below by rolling the session back via Forget().
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
		// Roll back the session that gotd wrote during auth — otherwise the
		// secret store and the DB drift apart and the user has to re-auth on
		// next login or manually clear the keychain. Forget() failures are
		// reported alongside the original error so the operator sees both.
		if forgetErr := sess.Forget(); forgetErr != nil {
			return fmt.Errorf("save account: %w (session rollback also failed: %v)", err, forgetErr)
		}
		return fmt.Errorf("save account: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "logged in as %s\n", phone); err != nil {
		return err
	}
	return nil
}
