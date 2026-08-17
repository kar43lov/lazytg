package sqlite_test

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func TestOutgoing_SaveGet_RoundTrip(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 100, Type: domain.ChatTypePrivate, Title: "Bob"}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	when := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	want := sqlite.OutgoingMessage{
		LocalID:   "abc-123",
		ChatID:    100,
		Text:      "hello world",
		ReplyTo:   42,
		State:     events.OutgoingStatePending,
		CreatedAt: when,
	}
	if err := repo.SaveOutgoing(ctx, want); err != nil {
		t.Fatalf("SaveOutgoing: %v", err)
	}
	got, err := repo.GetOutgoing(ctx, 100)
	if err != nil {
		t.Fatalf("GetOutgoing: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	g := got[0]
	if g.LocalID != want.LocalID || g.ChatID != want.ChatID || g.Text != want.Text ||
		g.ReplyTo != want.ReplyTo || g.State != want.State || !g.CreatedAt.Equal(when) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", g, want)
	}
}

func TestOutgoing_UpdateState_Sent(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 5, Type: domain.ChatTypePrivate}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := repo.SaveOutgoing(ctx, sqlite.OutgoingMessage{
		LocalID: "id-1", ChatID: 5, Text: "x", State: events.OutgoingStatePending,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := repo.UpdateOutgoingState(ctx, "id-1", events.OutgoingStateSent, 777, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	rows, err := repo.GetOutgoing(ctx, 5)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(rows) != 1 || rows[0].State != events.OutgoingStateSent || rows[0].ServerID != 777 || rows[0].Error != "" {
		t.Fatalf("state not updated: %+v", rows)
	}
}

func TestOutgoing_UpdateState_Failed(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 6, Type: domain.ChatTypePrivate}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := repo.SaveOutgoing(ctx, sqlite.OutgoingMessage{
		LocalID: "id-2", ChatID: 6, Text: "x", State: events.OutgoingStatePending,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := repo.UpdateOutgoingState(ctx, "id-2", events.OutgoingStateFailed, 0, "MESSAGE_TOO_LONG"); err != nil {
		t.Fatalf("update: %v", err)
	}
	rows, _ := repo.GetOutgoing(ctx, 6)
	if rows[0].State != events.OutgoingStateFailed || rows[0].Error != "MESSAGE_TOO_LONG" || rows[0].ServerID != 0 {
		t.Fatalf("failed state mismatch: %+v", rows[0])
	}
}

func TestOutgoing_UpdateState_NoSuchRow(t *testing.T) {
	repo, ctx := openTestRepo(t)
	err := repo.UpdateOutgoingState(ctx, "nope", events.OutgoingStateSent, 1, "")
	if err == nil {
		t.Fatalf("UpdateOutgoingState on missing row must error")
	}
}

func TestOutgoing_GetOutgoing_OrderByCreatedAt(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 8, Type: domain.ChatTypePrivate}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i, lid := range []string{"third", "first", "second"} {
		offset := []time.Duration{2 * time.Hour, 0, time.Hour}[i]
		if err := repo.SaveOutgoing(ctx, sqlite.OutgoingMessage{
			LocalID:   lid,
			ChatID:    8,
			Text:      lid,
			State:     events.OutgoingStatePending,
			CreatedAt: base.Add(offset),
		}); err != nil {
			t.Fatalf("save %s: %v", lid, err)
		}
	}
	rows, err := repo.GetOutgoing(ctx, 8)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d want 3", len(rows))
	}
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if rows[i].LocalID != w {
			t.Fatalf("row[%d].LocalID = %s want %s", i, rows[i].LocalID, w)
		}
	}
}

func TestOutgoing_Save_Validation(t *testing.T) {
	repo, ctx := openTestRepo(t)
	cases := []struct {
		name string
		m    sqlite.OutgoingMessage
	}{
		{"missing local_id", sqlite.OutgoingMessage{ChatID: 1, State: "pending"}},
		{"missing chat_id", sqlite.OutgoingMessage{LocalID: "x", State: "pending"}},
		{"missing state", sqlite.OutgoingMessage{LocalID: "x", ChatID: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := repo.SaveOutgoing(ctx, c.m); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
		})
	}
}
