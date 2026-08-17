package search

import "time"

// Query is the structured representation of a user-typed search expression
// after Parse has split out operators (from:/in:/before:/after:/has:),
// quoted phrases, exclusions and the residual free-text terms.
//
// Parser keeps From and InChats as the literal strings the user typed (with
// any leading @ or # already stripped). Resolution to chat IDs happens at
// SQL build time via a JOIN against the chats table, where the username
// column for private dialogs holds the @username and the title column
// holds the human-readable name. Stage 3 deliberately keeps the resolver
// in SQL rather than in Go so that the search service does not need a
// chat-lookup interface for every query (and so that an UPDATE to
// chats.username takes effect without restarting the service).
//
// Raw is preserved verbatim so the UI can re-render the original input
// in error messages and so that telemetry (when added in v0.2) can log
// what the user actually typed.
type Query struct {
	// Text holds the residual free-text terms joined by a single space.
	// FTS5 MATCH treats space as implicit AND, so "привет коллега"
	// requires both trigrams to be present.
	Text string

	// From lists the @usernames (without leading @) the user requested
	// via from: operators. Multiple from: operators are OR-combined at
	// SQL build time so "from:@alice from:@bob" matches either sender.
	From []string

	// InChats lists the chat usernames or titles (without leading #)
	// requested via in: operators. Multiple in: operators are OR-combined
	// in the SQL.
	InChats []string

	// Before is the upper bound (exclusive) for message date as parsed
	// from before:YYYY-MM-DD. nil means no upper bound.
	Before *time.Time

	// After is the lower bound (inclusive) for message date as parsed
	// from after:YYYY-MM-DD. nil means no lower bound.
	After *time.Time

	// HasFile is true when the user typed has:file. Stage 3 Task 3 only
	// records the flag — Task 6 adds the messages.media column and the
	// SQL filter that consumes it. BuildSQL emits no media-related WHERE
	// clause yet so historic queries keep working.
	HasFile bool

	// Phrases lists the contents of every "..." quoted token in the
	// input. They are folded into the FTS5 MATCH expression as quoted
	// phrases (FTS5 native phrase queries).
	Phrases []string

	// Excluded lists tokens that started with a single '-' (length ≥ 2,
	// so a bare '-' is not treated as an exclusion). Folded into the
	// FTS5 MATCH expression via a NOT clause at build time.
	Excluded []string

	// Raw is the verbatim input string Parse received, useful for
	// debugging and error messages.
	Raw string
}
