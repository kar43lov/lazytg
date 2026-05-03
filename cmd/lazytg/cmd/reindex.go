package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/core/search"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
)

// reindexProgressTimeout caps how long a single reindex pass can run.
// Real-world heavy users (1M+ messages across many chats) should still
// complete in well under an hour; the timeout is a safety net against
// a stuck VFS lock, not a deadline.
const reindexProgressTimeout = 1 * time.Hour

// newReindexCmd builds the `lazytg reindex` subcommand. It walks the
// FTS5 backfill pipeline outside of the TUI so heavy users with a
// historic database can prime the index up-front:
//
//	lazytg reindex --all              # all chats
//	lazytg reindex --chat 123456789   # one chat by id
//
// Progress is reported on stderr (the TUI is not started) so the
// command is friendly to scripting.
func newReindexCmd() *cobra.Command {
	var (
		flagAll  bool
		flagChat int64
	)
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Reindex local FTS5 search index (one chat or all chats)",
		Long: "reindex folds historic messages into the messages_fts virtual\n" +
			"table. Triggers in the schema already keep the index in sync for\n" +
			"every fresh write — this command is only needed for databases that\n" +
			"pre-date the FTS migration or after a manual messages_fts truncate.\n",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !flagAll && flagChat == 0 {
				return errors.New("reindex: pass --all or --chat <ID>")
			}
			if flagAll && flagChat != 0 {
				return errors.New("reindex: --all and --chat are mutually exclusive")
			}

			paths, err := resolvePathsOnly()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), reindexProgressTimeout)
			defer cancel()

			repo, err := sqlite.Open(ctx, dbPath(paths))
			if err != nil {
				return fmt.Errorf("reindex: open repo: %w", err)
			}
			defer func() { _ = repo.Close() }()

			logger := LoggerFromContext(ctx)
			indexer := search.NewIndexer(repo, logger)
			progressBus := newReindexProgressSink(cmd.ErrOrStderr())
			reindex := search.NewReindexService(indexer, repo, progressBus, logger, search.DefaultPerChatLimit)

			if flagAll {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "reindexing all chats…")
				return reindex.RunAll(ctx)
			}

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "reindexing chat %d…\n", flagChat)
			return reindex.Run(ctx, []int64{flagChat})
		},
	}
	cmd.Flags().BoolVar(&flagAll, "all", false, "reindex every chat (consumes more time on heavy databases)")
	cmd.Flags().Int64Var(&flagChat, "chat", 0, "reindex a single chat by id")
	return cmd
}

// reindexProgressSink prints ReindexProgress events to the supplied
// writer. Implements search.EventPublisher (the narrow Publish-only
// interface) so the CLI does not depend on a full *events.Bus.
type reindexProgressSink struct {
	w writeFlusher
}

// writeFlusher is the io.Writer slice we need; declared as an
// interface so tests can pass a *bytes.Buffer without wrapping it.
type writeFlusher interface {
	Write(p []byte) (int, error)
}

func newReindexProgressSink(w writeFlusher) *reindexProgressSink {
	return &reindexProgressSink{w: w}
}

func (s *reindexProgressSink) Publish(ev events.Event) {
	p, ok := ev.(events.ReindexProgress)
	if !ok {
		return
	}
	if p.Done {
		_, _ = fmt.Fprintf(s.w, "  done · total %d rows indexed\n", p.Total)
		return
	}
	_, _ = fmt.Fprintf(s.w, "  chat %d · %d rows · cumulative %d\n", p.ChatID, p.Indexed, p.Total)
}
