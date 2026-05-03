package search

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// dateLayouts is the ordered list of formats Parse will try for the
// before:/after: operators. The date-only form is the documented default
// (matches the plan example "before:2025-12-01"); the timestamp form is
// kept as a power-user escape hatch so people can narrow a search to a
// specific moment without having to crop the time off first.
var dateLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04:05",
}

// Parse turns a user-typed search string into a structured Query. The
// grammar is intentionally small so it can stay regex-free and easy to
// reason about for non-technical users:
//
//   - Whitespace splits tokens. Double-quoted segments ("...") count as a
//     single token; '\"' inside a quoted segment escapes the quote.
//   - A token of the form `key:value` where key is all-lowercase ASCII is
//     parsed as an operator (from/in/before/after/has). Unknown keys are
//     left as plain search terms — this keeps colon-heavy URLs in
//     queries from blowing up.
//   - Tokens beginning with '-' and of total length ≥ 2 are exclusions;
//     the leading '-' is stripped and the rest goes into Excluded.
//   - Otherwise, tokens become free-text search terms joined into Text.
//
// A purely-empty input (or one that boils down to no positive tokens
// after operators) returns an error so callers do not accidentally
// hand FTS5 a MATCH expression that would return every row.
func Parse(input string) (Query, error) {
	q := Query{Raw: input}

	tokens, err := tokenize(input)
	if err != nil {
		return q, err
	}

	var textTerms []string
	for _, tok := range tokens {
		if tok.quoted {
			if tok.value == "" {
				continue
			}
			q.Phrases = append(q.Phrases, tok.value)
			continue
		}
		if op, val, ok := splitOperator(tok.value); ok {
			handled, err := applyOperator(&q, op, val)
			if err != nil {
				return q, err
			}
			if handled {
				continue
			}
			// Unknown operator: keep the original "key:value" token so the
			// user's intent (e.g. searching for "RFC:7230") is preserved
			// even though the colon shape was "operator-like".
			textTerms = append(textTerms, tok.value)
			continue
		}
		if isExclusion(tok.value) {
			q.Excluded = append(q.Excluded, tok.value[1:])
			continue
		}
		if tok.value == "" {
			continue
		}
		textTerms = append(textTerms, tok.value)
	}

	q.Text = strings.Join(textTerms, " ")

	if q.Text == "" && len(q.Phrases) == 0 {
		// FTS5 MATCH cannot run with only a NOT clause and no positive
		// terms; reject before BuildSQL has to invent a fallback.
		if strings.TrimSpace(input) == "" {
			return q, errors.New("search: empty query")
		}
		return q, errors.New("search: no positive search terms (only operators or exclusions)")
	}

	return q, nil
}

// rawToken is the intermediate representation tokenize returns. quoted
// distinguishes a phrase ("foo bar") from a regular term so applyOperator
// does not accidentally treat a quoted "from:bar" as an operator.
type rawToken struct {
	value  string
	quoted bool
}

// tokenize splits input into terms, honouring "..." for phrase tokens and
// '\"' as an in-phrase escape. Any unbalanced opening quote is reported
// as an error pointing at its byte position so the UI can highlight it.
func tokenize(input string) ([]rawToken, error) {
	var (
		out       []rawToken
		current   strings.Builder
		inQuote   bool
		quoteOpen int
		hasToken  bool
	)
	flush := func(quoted bool) {
		if hasToken {
			out = append(out, rawToken{value: current.String(), quoted: quoted})
			current.Reset()
			hasToken = false
		}
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inQuote {
			if r == '\\' && i+1 < len(runes) && runes[i+1] == '"' {
				current.WriteRune('"')
				hasToken = true
				i++
				continue
			}
			if r == '"' {
				flush(true)
				inQuote = false
				continue
			}
			current.WriteRune(r)
			hasToken = true
			continue
		}
		if r == '"' {
			flush(false)
			inQuote = true
			quoteOpen = i
			hasToken = true // mark the empty-string phrase so flush(true) emits it; Parse drops it later
			continue
		}
		if unicode.IsSpace(r) {
			flush(false)
			continue
		}
		current.WriteRune(r)
		hasToken = true
	}

	if inQuote {
		return nil, fmt.Errorf("search: unclosed quote at position %d", quoteOpen)
	}
	flush(false)

	return out, nil
}

// splitOperator returns key/value if tok is shaped key:value where key is
// at least one lowercase ASCII letter and value is non-empty. Other
// shapes (e.g. http://example.com, 12:30) fall through as plain text.
func splitOperator(tok string) (key, value string, ok bool) {
	idx := strings.IndexByte(tok, ':')
	if idx <= 0 || idx == len(tok)-1 {
		return "", "", false
	}
	key = tok[:idx]
	for _, r := range key {
		if r < 'a' || r > 'z' {
			return "", "", false
		}
	}
	return key, tok[idx+1:], true
}

// applyOperator records a known operator on q and reports whether the
// token was consumed. handled=false means the key did not match any
// known operator; the caller treats the original token text as free
// text instead.
//
// The returned error is non-nil only for known keys whose value is
// invalid (e.g. before:not-a-date or has:everything). Unknown keys
// return (false, nil) so the caller can append the literal token into
// the free-text accumulator — this keeps URLs and timestamps in
// queries from breaking the parse.
func applyOperator(q *Query, key, value string) (handled bool, err error) {
	switch key {
	case "from":
		name := strings.TrimPrefix(value, "@")
		if name == "" {
			return true, fmt.Errorf("search: from:%s — username required (e.g. from:@alice)", value)
		}
		q.From = append(q.From, name)
		return true, nil
	case "in":
		name := strings.TrimPrefix(value, "#")
		if name == "" {
			return true, fmt.Errorf("search: in:%s — chat name required (e.g. in:#general)", value)
		}
		q.InChats = append(q.InChats, name)
		return true, nil
	case "before":
		t, perr := parseDate(value)
		if perr != nil {
			return true, fmt.Errorf("search: before:%s — %w", value, perr)
		}
		q.Before = &t
		return true, nil
	case "after":
		t, perr := parseDate(value)
		if perr != nil {
			return true, fmt.Errorf("search: after:%s — %w", value, perr)
		}
		q.After = &t
		return true, nil
	case "has":
		if value != "file" {
			return true, fmt.Errorf("search: has:%s — only has:file is supported", value)
		}
		q.HasFile = true
		return true, nil
	default:
		return false, nil
	}
}

// isExclusion reports whether tok is a `-foo`-style negation. Bare `-`
// (length 1) is left as plain text because it carries no negation
// payload.
func isExclusion(tok string) bool {
	return len(tok) >= 2 && tok[0] == '-'
}

// parseDate tries every layout in dateLayouts and returns the first
// successful Time in UTC. The error message hints at the expected
// formats so the user can correct without consulting docs.
func parseDate(s string) (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or YYYY-MM-DDTHH:MM:SS, got %q", s)
}
