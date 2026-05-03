package search

import (
	"errors"
	"fmt"
	"strings"
)

// BuildSQL converts a structured Query into the FTS5 MATCH expression
// (ftsMatch), the SQL fragment that goes into the JOIN/WHERE between
// messages_fts and messages (sqlText), and the bind args for the
// placeholders in sqlText. The returned sqlText is meant to be appended
// to a base query of the form:
//
//	SELECT m.*, snippet(...), bm25(messages_fts)
//	FROM messages_fts
//	JOIN messages m ON m.rowid = messages_fts.rowid
//	WHERE messages_fts MATCH ? <sqlText>
//	ORDER BY bm25(messages_fts)
//	LIMIT ?
//
// where the first ? is bound to ftsMatch and the LIMIT is bound to a
// caller-supplied limit. Args returned here only cover the placeholders
// inside sqlText; the caller prepends/append ftsMatch and limit itself
// when building the final []any slice.
//
// HasFile is intentionally not folded into sqlText at Stage 3 Task 3 —
// the messages.media columns land in Task 6, and emitting a WHERE
// against a non-existent column would explode every search call. The
// flag is preserved on Query so Task 6's BuildSQL extension can pick
// it up without a parser change.
func BuildSQL(q Query) (sqlText, ftsMatch string, args []any, err error) {
	ftsMatch, err = buildMatch(q)
	if err != nil {
		return "", "", nil, err
	}

	var (
		clauses []string
		params  []any
	)

	if len(q.From) > 0 {
		// from:@username resolves through chats.username — for private
		// dialogs the chat row id IS the user id (Telegram's data
		// model), so messages.from_id matches chats.id directly. We
		// use a subquery so the planner can use the chats username
		// index without a CROSS JOIN.
		placeholders, vals := bindStrings(q.From)
		clauses = append(clauses,
			fmt.Sprintf("m.from_id IN (SELECT id FROM chats WHERE username IN (%s))", placeholders))
		params = append(params, vals...)
	}

	if len(q.InChats) > 0 {
		// in:#chat resolves against either chats.username (the
		// canonical case for public chats and bots) or chats.title
		// (for private groups that have no username). The OR keeps
		// either kind of input working without forcing the user to
		// remember which is which.
		placeholders, vals := bindStrings(q.InChats)
		clauses = append(clauses, fmt.Sprintf(
			"m.chat_id IN (SELECT id FROM chats WHERE username IN (%s) OR title IN (%s))",
			placeholders, placeholders,
		))
		params = append(params, vals...)
		params = append(params, vals...)
	}

	if q.After != nil {
		clauses = append(clauses, "m.date >= ?")
		params = append(params, q.After.UTC().Unix())
	}
	if q.Before != nil {
		clauses = append(clauses, "m.date < ?")
		params = append(params, q.Before.UTC().Unix())
	}

	// HasFile: deliberately no-op until Task 6 lands the media columns.
	// We avoid silently dropping the user intent by surfacing a logical
	// note in the SQL comment so future code review/grep lands on the
	// right spot.
	if q.HasFile {
		clauses = append(clauses, "/* TODO(stage3-task6): m.media_type IS NOT NULL */ 1=1")
	}

	if len(clauses) == 0 {
		return "", ftsMatch, nil, nil
	}
	return " AND " + strings.Join(clauses, " AND "), ftsMatch, params, nil
}

// buildMatch composes the FTS5 MATCH expression. Plain Text tokens
// already use FTS5's implicit AND between space-separated terms.
// Phrases are wrapped in their canonical "..." form. Excluded terms
// fold into a trailing NOT (a OR b OR c) clause, which FTS5 supports
// natively.
//
// The function returns an error when the resulting expression has no
// positive operand, since FTS5 cannot run a pure NOT query — Parse
// already guards against this, but BuildSQL stays defensive in case a
// future caller hand-constructs a Query.
func buildMatch(q Query) (string, error) {
	var parts []string
	if q.Text != "" {
		parts = append(parts, q.Text)
	}
	for _, p := range q.Phrases {
		parts = append(parts, fmt.Sprintf("%q", p))
	}
	if len(parts) == 0 {
		return "", errors.New("search: empty MATCH expression (no Text or Phrases)")
	}
	positive := strings.Join(parts, " ")

	if len(q.Excluded) == 0 {
		return positive, nil
	}
	excluded := strings.Join(q.Excluded, " OR ")
	return fmt.Sprintf("(%s) NOT (%s)", positive, excluded), nil
}

// bindStrings turns a string slice into a comma-separated list of `?`
// placeholders together with the matching []any args. Returns "?,?,?"
// for a 3-element slice etc. Empty input yields "" — callers should
// guard with len(...) > 0 before calling.
func bindStrings(in []string) (string, []any) {
	if len(in) == 0 {
		return "", nil
	}
	placeholders := strings.Repeat("?,", len(in))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(in))
	for i, v := range in {
		args[i] = v
	}
	return placeholders, args
}
