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
// HasFile is folded into a "m.media_kind IS NOT NULL" filter — the
// media_kind column was added by migration 0008 in Stage 3 Task 6 and
// is NULL for plain-text messages.
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

	// HasFile becomes a real predicate now that Task 6 added the
	// messages.media_kind column (migration 0008). NULL in that column
	// means "plain text", any non-NULL value is a media-bearing
	// message — the simplest possible filter and one the SQLite
	// query planner handles via the existing PK index without a
	// dedicated covering index.
	if q.HasFile {
		clauses = append(clauses, "m.media_kind IS NOT NULL")
	}

	if len(clauses) == 0 {
		return "", ftsMatch, nil, nil
	}
	return " AND " + strings.Join(clauses, " AND "), ftsMatch, params, nil
}

// buildMatch composes the FTS5 MATCH expression. Every user-supplied
// token (text term, phrase, exclusion) is wrapped in FTS5 phrase
// quotes with embedded double-quotes doubled — this neutralises FTS5
// grammar tokens (AND/OR/NOT/NEAR, '*', '^', '(', ')', ':') in the
// raw input so a user typing "name OR delete" searches for the
// literal trigrams instead of triggering the FTS5 OR operator. Under
// the trigram tokenizer the quoted form matches the same character
// n-grams as the unquoted form, so quoting is free in terms of
// recall.
//
// Multiple text terms join with a space (FTS5 implicit AND). Phrases
// follow the same join rule; the user already separated them. Excluded
// terms fold into a trailing NOT (a OR b OR c) clause.
//
// Returns an error when the resulting expression has no positive
// operand — FTS5 cannot run a pure NOT query. Parse already guards
// this, but BuildSQL stays defensive in case a future caller
// hand-constructs a Query.
func buildMatch(q Query) (string, error) {
	var parts []string
	if q.Text != "" {
		for _, term := range strings.Fields(q.Text) {
			parts = append(parts, ftsQuote(term))
		}
	}
	for _, p := range q.Phrases {
		parts = append(parts, ftsQuote(p))
	}
	if len(parts) == 0 {
		return "", errors.New("search: empty MATCH expression (no Text or Phrases)")
	}
	positive := strings.Join(parts, " ")

	if len(q.Excluded) == 0 {
		return positive, nil
	}
	excluded := make([]string, len(q.Excluded))
	for i, e := range q.Excluded {
		excluded[i] = ftsQuote(e)
	}
	return fmt.Sprintf("(%s) NOT (%s)", positive, strings.Join(excluded, " OR ")), nil
}

// ftsQuote wraps s as an FTS5 phrase token. SQLite FTS5 escapes an
// embedded double-quote by doubling it ("a""b"), unlike Go's %q which
// uses a backslash. Quoting also turns FTS5 grammar tokens (AND/OR/
// NOT/NEAR, parentheses, colons, '*', '^') into literal trigrams,
// which is what we want for user input.
func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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
