package search

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// DefaultRemoteLimit is how many hits one server search asks for.
const DefaultRemoteLimit = 20

// ErrRemoteNeedsText is returned for a query with nothing the server
// can search — only filters, or nothing at all.
var ErrRemoteNeedsText = errors.New("the server searches words; type some")

// ErrRemoteLocalFilters is returned when the query carries operators
// only the local index understands. Dropping them silently would turn
// "in:#work budget" into a search of every chat, which is not what the
// user asked for.
var ErrRemoteLocalFilters = errors.New("from:, in:, has: and -word are local-only filters")

// RemoteQuery is what the server is asked. Text is the free-text part
// of the user's query; After and Before are optional bounds.
type RemoteQuery struct {
	Text   string
	After  time.Time
	Before time.Time
	Limit  int
}

// RemoteResult is what came back: the messages and the conversations
// they live in, so an unlisted chat can be listed before the jump.
type RemoteResult struct {
	Messages []domain.Message
	Chats    []domain.Chat
	Peers    []domain.Peer
}

// RemoteSearcher runs one search request against the server.
type RemoteSearcher interface {
	SearchGlobal(ctx context.Context, q RemoteQuery) (RemoteResult, error)
}

// RemoteStore takes the hits into the mirror. The chat row first: a
// message references its chat, and the jump reads the message back
// from the mirror through JumpContext.
type RemoteStore interface {
	SaveChatIfMissing(ctx context.Context, c domain.Chat) error
	SaveMessage(ctx context.Context, m domain.Message) error
}

// RemotePeerStore keeps the access hash a later history fetch needs.
type RemotePeerStore interface {
	Save(ctx context.Context, p domain.Peer) error
}

// RemoteService is the server-side fallback of the search overlay: the
// same query syntax as the local index, one request per explicit ask,
// and every hit written into the mirror so the jump and the next local
// search find it without the server.
type RemoteService struct {
	remote RemoteSearcher
	store  RemoteStore
	peers  RemotePeerStore
	log    *slog.Logger
}

// NewRemoteService wires the fallback. peers may be nil.
func NewRemoteService(remote RemoteSearcher, store RemoteStore, peers RemotePeerStore, log *slog.Logger) *RemoteService {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &RemoteService{remote: remote, store: store, peers: peers, log: log}
}

// Search parses raw the way the local service does, refuses what the
// server cannot honour, runs the request and mirrors the hits.
func (r *RemoteService) Search(ctx context.Context, raw string, limit int) ([]Hit, error) {
	if r == nil || r.remote == nil {
		return nil, errors.New("server search: not connected")
	}
	q, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if len(q.From) > 0 || len(q.InChats) > 0 || q.HasFile || len(q.Excluded) > 0 {
		return nil, ErrRemoteLocalFilters
	}
	text := strings.TrimSpace(strings.Join(append([]string{q.Text}, q.Phrases...), " "))
	if text == "" {
		return nil, ErrRemoteNeedsText
	}
	if limit <= 0 {
		limit = DefaultRemoteLimit
	}
	rq := RemoteQuery{Text: text, Limit: limit}
	if q.After != nil {
		rq.After = *q.After
	}
	if q.Before != nil {
		rq.Before = *q.Before
	}
	res, err := r.remote.SearchGlobal(ctx, rq)
	if err != nil {
		return nil, err
	}
	r.mirror(ctx, res)

	hits := make([]Hit, 0, len(res.Messages))
	for _, m := range res.Messages {
		hits = append(hits, Hit{
			Message: m,
			Snippet: remoteSnippet(m.Text, text),
			ChatID:  m.ChatID,
			Remote:  true,
		})
	}
	return hits, nil
}

// mirror writes what the server returned into the local store. A
// failure is logged, not returned: the hit is still shown, and the
// jump falls back to opening the chat without the context window.
func (r *RemoteService) mirror(ctx context.Context, res RemoteResult) {
	if r.store == nil {
		return
	}
	for _, p := range res.Peers {
		if r.peers == nil {
			break
		}
		if err := r.peers.Save(ctx, p); err != nil {
			r.log.Warn("search: remote peer not saved", "peer_id", p.ID, "err", err)
		}
	}
	for _, c := range res.Chats {
		if err := r.store.SaveChatIfMissing(ctx, c); err != nil {
			r.log.Warn("search: remote chat not saved", "chat_id", c.ID, "err", err)
		}
	}
	for _, m := range res.Messages {
		if err := r.store.SaveMessage(ctx, m); err != nil {
			r.log.Warn("search: remote hit not saved", "chat_id", m.ChatID, "message_id", m.ID, "err", err)
		}
	}
}

// remoteSnippet marks the first occurrence of needle in text with the
// same <b>…</b> markers FTS5 uses, cut to a window around it. The
// server does not send snippets, and a hit whose match is off-screen
// looks like a wrong hit. With no occurrence (the server matched a
// word form, or another word of the query) the head of the text is
// returned unmarked.
func remoteSnippet(text, needle string) string {
	const before, after = 40, 60
	rt := []rune(text)
	lt := []rune(strings.ToLower(text))
	ln := []rune(strings.ToLower(strings.TrimSpace(needle)))
	if len(rt) != len(lt) || len(ln) == 0 {
		return head(rt, before+after)
	}
	idx := runeIndex(lt, ln)
	if idx < 0 {
		for _, word := range strings.Fields(string(ln)) {
			if idx = runeIndex(lt, []rune(word)); idx >= 0 {
				ln = []rune(word)
				break
			}
		}
	}
	if idx < 0 {
		return head(rt, before+after)
	}
	start := max(idx-before, 0)
	end := min(idx+len(ln)+after, len(rt))
	var b strings.Builder
	if start > 0 {
		b.WriteString("...")
	}
	b.WriteString(string(rt[start:idx]))
	b.WriteString("<b>")
	b.WriteString(string(rt[idx : idx+len(ln)]))
	b.WriteString("</b>")
	b.WriteString(string(rt[idx+len(ln) : end]))
	if end < len(rt) {
		b.WriteString("...")
	}
	return b.String()
}

func runeIndex(hay, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func head(rt []rune, n int) string {
	if len(rt) <= n {
		return string(rt)
	}
	return string(rt[:n]) + "..."
}
