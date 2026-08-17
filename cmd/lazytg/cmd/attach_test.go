package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/app"
	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// openTestApp yields an App backed by a throwaway SQLite file. Only the fields
// the attach helpers touch are populated — Build would drag in the permissions
// audit and keymap loading, neither of which is under test here.
func openTestApp(t *testing.T) *app.App {
	t.Helper()
	repo, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "lazytg.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return &app.App{Repo: repo}
}

func TestResolveAttachPhone_PrefersFlag(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)
	if err := rt.Repo.SaveAccount(context.Background(), domain.Account{Phone: "+79990000001"}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	flagAccount = "+79995550000"

	got, err := resolveAttachPhone(context.Background(), rt)
	if err != nil {
		t.Fatalf("resolveAttachPhone: %v", err)
	}
	if got != "+79995550000" {
		t.Fatalf("got %q, want the --account value", got)
	}
}

func TestResolveAttachPhone_SingleAccountNeedsNoFlag(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)
	if err := rt.Repo.SaveAccount(context.Background(), domain.Account{Phone: "+79990000002"}); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	got, err := resolveAttachPhone(context.Background(), rt)
	if err != nil {
		t.Fatalf("resolveAttachPhone: %v", err)
	}
	if got != "+79990000002" {
		t.Fatalf("got %q, want the only logged-in account", got)
	}
}

// With no account the TUI must fall back to the cache, and the reason has to
// name the command that fixes it — this string is what the user sees in the log
// when the chat list is empty.
func TestResolveAttachPhone_NoAccountExplainsHowToFix(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)

	_, err := resolveAttachPhone(context.Background(), rt)
	if err == nil {
		t.Fatalf("want an error when no account is logged in")
	}
	if !strings.Contains(err.Error(), "lazytg login") {
		t.Fatalf("error must point at `lazytg login`, got %q", err)
	}
}

// Guessing between accounts would send messages from an identity the user did
// not pick, so ambiguity is an error rather than a default.
func TestResolveAttachPhone_RefusesToGuessBetweenAccounts(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)
	for _, phone := range []string{"+79990000003", "+79990000004"} {
		if err := rt.Repo.SaveAccount(context.Background(), domain.Account{Phone: phone}); err != nil {
			t.Fatalf("seed account %s: %v", phone, err)
		}
	}

	_, err := resolveAttachPhone(context.Background(), rt)
	if err == nil {
		t.Fatalf("want an error when several accounts are logged in")
	}
	if !strings.Contains(err.Error(), "--account") {
		t.Fatalf("error must name --account, got %q", err)
	}
}

// Returning a typed nil would hand the panes a non-nil interface wrapping a nil
// pointer, defeating their `== nil` guards: the first Enter in the composer
// would panic instead of doing nothing.
func TestOfflineProvidersAreNilInterfaces(t *testing.T) {
	t.Parallel()
	rt := &app.App{}

	if sender := composerSender(rt); sender != nil {
		t.Errorf("composerSender must be a nil interface when offline, got %#v", sender)
	}
	if provider := threadHistoryProvider(rt); provider != nil {
		t.Errorf("threadHistoryProvider must be a nil interface when offline, got %#v", provider)
	}
}

// attachTelegram is called before the UI exists and must never abort startup.
// With no account it should return promptly and leave the runtime offline.
//
// The returned stop function must be non-nil on every path — runTUI defers it
// unconditionally, so a nil here would panic on exit instead of failing at the
// point of the problem.
func TestAttachTelegram_NoAccountLeavesRuntimeOffline(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)

	stop := attachTelegram(context.Background(), rt, buildLogger(0, false))
	if stop == nil {
		t.Fatalf("stop must never be nil — runTUI defers it unconditionally")
	}
	stop()
	stop() // idempotent: context cancellation tolerates repeat calls

	if rt.Client != nil || rt.Sender != nil || rt.Dialogs != nil {
		t.Fatalf("no account must leave MTProto services unset: client=%v sender=%v dialogs=%v",
			rt.Client != nil, rt.Sender != nil, rt.Dialogs != nil)
	}
}

// A cancelled context must not leave the caller without a usable stop
// function either.
func TestAttachTelegram_CancelledContextStillReturnsStop(t *testing.T) {
	setupCmdTest(t)
	rt := openTestApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stop := attachTelegram(ctx, rt, buildLogger(0, false))
	if stop == nil {
		t.Fatalf("stop must never be nil")
	}
	stop()
}
