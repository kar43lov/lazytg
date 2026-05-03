package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/pgmac/lazytg/internal/core/events"
)

// SenderInterface is the gotd-free contract SendService relies on. It is
// satisfied by *internal/tg.Sender in production and by mocks in tests;
// the signature mirrors the MTProto call (chat id + text + reply id) and
// returns the server-assigned message id on success.
type SenderInterface interface {
	SendText(ctx context.Context, chatID int64, text string, replyTo int) (serverID int64, err error)
}

// RateLimiter is the optional throttle SendService consults before each
// MTProto attempt. Nil is allowed — the deliver loop short-circuits the
// Wait call so unit tests of unrelated behaviour stay free of plumbing.
//
// Production wiring satisfies this with internal/core/security.SendGuard,
// a TokenBucket(rate=10/sec, burst=30) wrapper. Lives behind an interface
// (rather than a concrete type) so internal/core/security can depend on
// internal/core/sync without forcing the reverse import cycle.
type RateLimiter interface {
	Wait(ctx context.Context) error
}

// OutgoingStore is the storage surface SendService writes through. The
// methods are a subset of *sqlite.Repo so unit tests can swap in an
// in-memory fake. CreatedAt on the saved record is the moment SendText
// was called, not the moment the server confirmed delivery, so the UI
// can render an order-stable list while sends are still pending.
type OutgoingStore interface {
	SaveOutgoing(ctx context.Context, m OutgoingRecord) error
	UpdateOutgoingState(ctx context.Context, localID, state string, serverID int64, errMsg string) error
}

// OutgoingRecord is the gotd-free projection of an in-flight send. It is
// declared here (not imported from sqlite) so the sync package stays free
// of database/sql types — mirrors the shape of sqlite.OutgoingMessage.
type OutgoingRecord struct {
	LocalID   string
	ChatID    int64
	Text      string
	ReplyTo   int64
	State     string
	CreatedAt time.Time
}

// SendConfig groups the optional retry knobs so the constructor signature
// stays stable as we tune defaults. Zero values use the documented
// production defaults.
type SendConfig struct {
	// MaxNetworkRetries is how many times SendService retries an RPC
	// after a non-validation, non-FLOOD_WAIT error before giving up.
	// Default: 3.
	MaxNetworkRetries int
	// InitialBackoff is the wait before the first retry. Subsequent
	// retries double the wait (capped at BackoffCap). Default: 500ms.
	InitialBackoff time.Duration
	// BackoffCap caps the per-retry wait. Default: 8s — enough to ride
	// out a typical reconnect without crossing the user's patience
	// threshold for a single message.
	BackoffCap time.Duration
}

func (c *SendConfig) defaults() {
	if c.MaxNetworkRetries <= 0 {
		c.MaxNetworkRetries = 3
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = 500 * time.Millisecond
	}
	if c.BackoffCap <= 0 {
		c.BackoffCap = 8 * time.Second
	}
}

// SendService owns the optimistic-UI lifecycle of an outgoing message:
// SaveOutgoing(state="pending") + bus event, hand off to the sender in a
// background goroutine, then update the record + emit a follow-up event
// when the server replies (or all retries are exhausted). The struct is
// safe for concurrent SendText calls — every call spawns its own
// goroutine, scoped to the service-level background context (see
// WithBackgroundContext).
type SendService struct {
	sender  SenderInterface
	store   OutgoingStore
	bus     *events.Bus
	log     *slog.Logger
	cfg     SendConfig
	limiter RateLimiter

	// bgCtx is the lifetime ctx used for the deliver goroutine and the
	// markSent / markFailed storage writes. It is intentionally NOT the
	// caller's ctx because the typical caller (input pane closure in the
	// TUI) returns immediately after SendText, and a defer-cancel on
	// the closure ctx would otherwise abort every in-flight retry the
	// instant SendText returns. WithBackgroundContext lets the wiring
	// layer plumb through a runtime-scoped ctx so all in-flight sends
	// shut down cleanly on TUI exit.
	bgCtx context.Context

	now    func() time.Time
	newID  func() string
	jitter func() float64 // [0,1) — used to scatter exponential backoff
}

// NewSendService wires a SendService. log may be nil; a no-op logger is
// used in that case so unit tests stay free of plumbing noise.
//
// The deliver goroutine starts with context.Background as its lifetime;
// callers that want a bounded service lifetime (production wiring) should
// chain WithBackgroundContext before handing the service to consumers.
func NewSendService(sender SenderInterface, store OutgoingStore, bus *events.Bus, log *slog.Logger, cfg SendConfig) *SendService {
	cfg.defaults()
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &SendService{
		sender: sender,
		store:  store,
		bus:    bus,
		log:    log,
		cfg:    cfg,
		bgCtx:  context.Background(),
		now:    time.Now,
		newID:  uuid.NewString,
		jitter: rand.Float64,
	}
}

// WithRateLimiter installs the optional pre-send throttle. The deliver
// loop calls limiter.Wait(ctx) before every attempt (including retries
// that follow a transient network error) so a sustained burst of
// retries cannot cumulatively exceed the configured ceiling. A nil
// argument disables throttling — used by unit tests that want to drive
// the retry policy without a clock dependency.
//
// Returns the same service so the wiring layer can chain
// WithRateLimiter().WithBackgroundContext(ctx) in one line.
func (s *SendService) WithRateLimiter(limiter RateLimiter) *SendService {
	s.limiter = limiter
	return s
}

// WithBackgroundContext sets the lifetime ctx for the deliver goroutines.
// The wiring layer typically passes a ctx scoped to the TUI session so
// every in-flight send aborts cleanly on shutdown. Returns the same
// service for fluent chaining.
//
// Calling with a nil ctx is a no-op — the previous (or default
// context.Background) ctx is preserved so a misconfigured caller cannot
// silently break the deliver goroutine.
func (s *SendService) WithBackgroundContext(ctx context.Context) *SendService {
	if ctx != nil {
		s.bgCtx = ctx
	}
	return s
}

// SendText opens a new outgoing-message lifecycle: persist the optimistic
// record, emit a "pending" bus event, and kick off the actual RPC in a
// background goroutine. The function returns as soon as the optimistic
// record is in place (or the storage write fails) so the UI can render
// the message immediately.
//
// ctx scopes the synchronous SaveOutgoing call only; the deliver
// goroutine uses the service-level bgCtx instead. This separation
// matters because the typical caller — the input-pane handler in the
// TUI — runs inside a tea.Cmd closure that defer-cancels its own ctx
// when the closure returns. If the deliver goroutine inherited that ctx,
// every send would be cancelled before the first sender RPC completes.
// The service-level bgCtx (set via WithBackgroundContext) lives for the
// TUI session, so deliver can run its full retry loop and only aborts on
// program shutdown.
func (s *SendService) SendText(ctx context.Context, chatID int64, text string, replyTo int) (string, error) {
	if chatID == 0 {
		return "", errors.New("send: chat_id is required")
	}
	if text == "" {
		return "", errors.New("send: text is empty")
	}
	localID := s.newID()
	record := OutgoingRecord{
		LocalID:   localID,
		ChatID:    chatID,
		Text:      text,
		ReplyTo:   int64(replyTo),
		State:     events.OutgoingStatePending,
		CreatedAt: s.now().UTC(),
	}
	if err := s.store.SaveOutgoing(ctx, record); err != nil {
		return "", fmt.Errorf("send: persist optimistic chat=%d: %w", chatID, err)
	}
	s.publish(events.OutgoingMessageStateChanged{
		LocalID: localID,
		ChatID:  chatID,
		State:   events.OutgoingStatePending,
	})
	go s.deliver(s.bgCtx, localID, chatID, text, replyTo)
	return localID, nil
}

// deliver is the background goroutine that owns the retry policy. It
// tries to call the underlying sender up to MaxNetworkRetries+1 times,
// honouring FLOOD_WAIT pauses verbatim and giving up on validation errors
// without retry. Every terminal transition writes back to storage and
// publishes the matching bus event, which is what drives the UI from
// pending → sent | failed.
func (s *SendService) deliver(ctx context.Context, localID string, chatID int64, text string, replyTo int) {
	backoff := s.cfg.InitialBackoff
	for attempt := 0; ; attempt++ {
		if !s.awaitToken(ctx, localID, chatID) {
			s.markFailed(ctx, localID, chatID, fmt.Sprintf("cancelled before send: %v", ctx.Err()))
			return
		}
		serverID, err := s.sender.SendText(ctx, chatID, text, replyTo)
		if err == nil {
			s.markSent(ctx, localID, chatID, serverID)
			return
		}
		if d, ok := AsFloodWait(err); ok {
			// Defensive floor: a server (or buggy mock) returning
			// FLOOD_WAIT with retry_after <= 0 would otherwise feed
			// s.sleep(ctx, 0) which returns immediately, and the loop
			// would spin at the SendGuard ceiling (10 msg/sec) without
			// ever incrementing `attempt`. We clamp non-positive
			// durations to 1 s while honouring any positive value the
			// server provided — including the millisecond-scale values
			// tests inject through scriptedSender.
			if d <= 0 {
				d = time.Second
			}
			s.log.Warn("send: flood wait",
				"chat_id", chatID, "local_id", localID, "retry_after", d)
			if !s.sleep(ctx, d) {
				s.markFailed(ctx, localID, chatID, fmt.Sprintf("cancelled during flood-wait: %v", ctx.Err()))
				return
			}
			continue
		}
		if reason, ok := AsValidation(err); ok {
			s.log.Warn("send: validation",
				"chat_id", chatID, "local_id", localID, "reason", reason)
			s.markFailed(ctx, localID, chatID, reason)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.markFailed(ctx, localID, chatID, err.Error())
			return
		}
		if attempt >= s.cfg.MaxNetworkRetries {
			s.log.Error("send: retries exhausted",
				"chat_id", chatID, "local_id", localID, "err", err, "attempts", attempt+1)
			s.markFailed(ctx, localID, chatID, err.Error())
			return
		}
		wait := jitterDuration(backoff, s.jitter())
		s.log.Warn("send: transient error, retrying",
			"chat_id", chatID, "local_id", localID, "err", err, "attempt", attempt+1, "wait", wait)
		if !s.sleep(ctx, wait) {
			s.markFailed(ctx, localID, chatID, fmt.Sprintf("cancelled during backoff: %v", ctx.Err()))
			return
		}
		backoff = nextBackoff(backoff, s.cfg.BackoffCap)
	}
}

// awaitToken blocks on the optional rate-limiter, logging a warn line
// whenever the wait actually stalls the send. Returns false iff ctx was
// cancelled mid-wait — the caller treats that as a terminal "cancelled
// before send" failure (we do not silently swallow user cancellation).
//
// The 1 ms threshold filters the warm-bucket case: when the limiter
// has tokens available, Wait returns "instantly" and we don't want a
// log line for every send.
func (s *SendService) awaitToken(ctx context.Context, localID string, chatID int64) bool {
	if s.limiter == nil {
		return true
	}
	start := s.now()
	if err := s.limiter.Wait(ctx); err != nil {
		s.log.Warn("send: rate-limit Wait cancelled",
			"chat_id", chatID, "local_id", localID, "err", err)
		return false
	}
	if waited := s.now().Sub(start); waited > time.Millisecond {
		s.log.Warn("send: rate-limit hit",
			"chat_id", chatID, "local_id", localID, "wait", waited)
	}
	return true
}

// markSent persists the success transition and publishes the matching
// event. Storage failures here only log: the user already saw the
// optimistic preview and a stale "pending" pill is preferable to a
// dropped message that vanishes from the UI on next reload.
func (s *SendService) markSent(ctx context.Context, localID string, chatID int64, serverID int64) {
	if err := s.store.UpdateOutgoingState(ctx, localID, events.OutgoingStateSent, serverID, ""); err != nil {
		s.log.Error("send: persist sent state failed",
			"chat_id", chatID, "local_id", localID, "err", err)
	}
	s.publish(events.OutgoingMessageStateChanged{
		LocalID:  localID,
		ChatID:   chatID,
		ServerID: serverID,
		State:    events.OutgoingStateSent,
	})
}

// markFailed persists the failure transition and publishes the matching
// event. Like markSent, storage failures degrade gracefully — the bus
// event still fires so the UI can render the [✗] pill from in-memory
// state alone.
func (s *SendService) markFailed(ctx context.Context, localID string, chatID int64, reason string) {
	if err := s.store.UpdateOutgoingState(ctx, localID, events.OutgoingStateFailed, 0, reason); err != nil {
		s.log.Error("send: persist failed state failed",
			"chat_id", chatID, "local_id", localID, "err", err)
	}
	s.publish(events.OutgoingMessageStateChanged{
		LocalID: localID,
		ChatID:  chatID,
		State:   events.OutgoingStateFailed,
		Error:   reason,
	})
}

// sleep waits d unless ctx is cancelled first. Returns true on a clean
// wake-up, false on cancellation.
func (s *SendService) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// publish is a nil-safe wrapper around Bus.Publish so SendService can be
// constructed without a bus in the smallest possible test setup.
func (s *SendService) publish(e events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(e)
}

// nextBackoff doubles d, capped at maxWait. Pulled out so the unit test
// can pin the doubling behaviour without driving SendService end-to-end.
func nextBackoff(d, maxWait time.Duration) time.Duration {
	next := d * 2
	if next > maxWait {
		next = maxWait
	}
	return next
}

// jitterDuration scatters d by ±10% using j ∈ [0,1) so two failing sends
// from the same chat do not retry in lockstep.
func jitterDuration(d time.Duration, j float64) time.Duration {
	if d <= 0 {
		return 0
	}
	delta := float64(d) * 0.1 * (j*2 - 1)
	return d + time.Duration(delta)
}
