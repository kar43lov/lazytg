package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/updates"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func openStateRepo(t *testing.T, accountID int64) (*sqlite.StateRepo, *sqlite.Repo, context.Context) {
	t.Helper()
	repo, ctx := openTestRepo(t)
	if err := repo.SaveAccount(ctx, domain.Account{ID: accountID, Phone: "+10000000001"}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	return sqlite.NewStateRepo(repo.DB()), repo, ctx
}

func TestStateRepo_GetState_AbsentReturnsFalse(t *testing.T) {
	state, _, ctx := openStateRepo(t, 42)
	got, found, err := state.GetState(ctx, 42)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if found {
		t.Fatalf("found=true on a fresh DB; got=%+v", got)
	}
}

func TestStateRepo_SetGetState_Roundtrip(t *testing.T) {
	state, _, ctx := openStateRepo(t, 7)

	want := updates.State{Pts: 100, Qts: 50, Date: 1700000000, Seq: 5}
	if err := state.SetState(ctx, 7, want); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	got, found, err := state.GetState(ctx, 7)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !found {
		t.Fatalf("found=false after SetState")
	}
	if got != want {
		t.Fatalf("state mismatch: got=%+v want=%+v", got, want)
	}
}

func TestStateRepo_PartialUpdates(t *testing.T) {
	state, _, ctx := openStateRepo(t, 9)

	if err := state.SetState(ctx, 9, updates.State{Pts: 1, Qts: 2, Date: 3, Seq: 4}); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := state.SetPts(ctx, 9, 11); err != nil {
		t.Fatalf("SetPts: %v", err)
	}
	if err := state.SetQts(ctx, 9, 22); err != nil {
		t.Fatalf("SetQts: %v", err)
	}
	if err := state.SetDate(ctx, 9, 33); err != nil {
		t.Fatalf("SetDate: %v", err)
	}
	if err := state.SetSeq(ctx, 9, 44); err != nil {
		t.Fatalf("SetSeq: %v", err)
	}
	got, _, err := state.GetState(ctx, 9)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	want := updates.State{Pts: 11, Qts: 22, Date: 33, Seq: 44}
	if got != want {
		t.Fatalf("state mismatch: got=%+v want=%+v", got, want)
	}
}

func TestStateRepo_SetDateSeq_Atomic(t *testing.T) {
	state, _, ctx := openStateRepo(t, 11)
	if err := state.SetState(ctx, 11, updates.State{Pts: 1, Qts: 2, Date: 3, Seq: 4}); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := state.SetDateSeq(ctx, 11, 999, 888); err != nil {
		t.Fatalf("SetDateSeq: %v", err)
	}
	got, _, err := state.GetState(ctx, 11)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if got.Date != 999 || got.Seq != 888 {
		t.Fatalf("date/seq not updated: %+v", got)
	}
}

func TestStateRepo_SetWithoutInit_ReturnsError(t *testing.T) {
	state, _, ctx := openStateRepo(t, 13)
	if err := state.SetPts(ctx, 13, 1); err == nil {
		t.Fatalf("SetPts on missing row must error")
	}
	if err := state.SetDateSeq(ctx, 13, 1, 2); err == nil {
		t.Fatalf("SetDateSeq on missing row must error")
	}
}

func TestStateRepo_ChannelPts_Roundtrip(t *testing.T) {
	state, _, ctx := openStateRepo(t, 17)

	pts, found, err := state.GetChannelPts(ctx, 17, 1001)
	if err != nil {
		t.Fatalf("GetChannelPts: %v", err)
	}
	if found {
		t.Fatalf("expected absent, got pts=%d", pts)
	}
	if err := state.SetChannelPts(ctx, 17, 1001, 555); err != nil {
		t.Fatalf("SetChannelPts: %v", err)
	}
	if err := state.SetChannelPts(ctx, 17, 1002, 777); err != nil {
		t.Fatalf("SetChannelPts(1002): %v", err)
	}
	if err := state.SetChannelPts(ctx, 17, 1001, 600); err != nil {
		t.Fatalf("SetChannelPts(update): %v", err)
	}
	got, found, err := state.GetChannelPts(ctx, 17, 1001)
	if err != nil {
		t.Fatalf("GetChannelPts: %v", err)
	}
	if !found || got != 600 {
		t.Fatalf("expected pts=600, got found=%v pts=%d", found, got)
	}
}

func TestStateRepo_ForEachChannels(t *testing.T) {
	state, _, ctx := openStateRepo(t, 23)
	want := map[int64]int{1: 10, 2: 20, 3: 30}
	for ch, pts := range want {
		if err := state.SetChannelPts(ctx, 23, ch, pts); err != nil {
			t.Fatalf("SetChannelPts(%d): %v", ch, err)
		}
	}
	seen := make(map[int64]int)
	err := state.ForEachChannels(ctx, 23, func(_ context.Context, channelID int64, pts int) error {
		seen[channelID] = pts
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachChannels: %v", err)
	}
	if len(seen) != len(want) {
		t.Fatalf("seen=%v want=%v", seen, want)
	}
	for ch, pts := range want {
		if seen[ch] != pts {
			t.Fatalf("channel %d pts mismatch: got=%d want=%d", ch, seen[ch], pts)
		}
	}
}

func TestStateRepo_ForEachChannels_StopsOnCallbackError(t *testing.T) {
	state, _, ctx := openStateRepo(t, 25)
	for ch := int64(1); ch <= 3; ch++ {
		if err := state.SetChannelPts(ctx, 25, ch, int(ch*10)); err != nil {
			t.Fatalf("SetChannelPts: %v", err)
		}
	}
	stopErr := errors.New("stop")
	count := 0
	err := state.ForEachChannels(ctx, 25, func(_ context.Context, _ int64, _ int) error {
		count++
		return stopErr
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("expected stopErr, got %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 callback before stop, got %d", count)
	}
}
