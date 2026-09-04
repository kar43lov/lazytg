package chats

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func folderChats() []ChatItem {
	base := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	return []ChatItem{
		NewChatItem(domain.Chat{ID: 1, Title: "Ada", Type: domain.ChatTypePrivate, LastMessageDate: base}, ""),
		NewChatItem(domain.Chat{ID: 2, Title: "Team", Type: domain.ChatTypeSupergroup, LastMessageDate: base}, ""),
		NewChatItem(domain.Chat{ID: 3, Title: "News", Type: domain.ChatTypeChannel, LastMessageDate: base}, ""),
	}
}

func modelWithFolders(t *testing.T, folders []domain.Folder) Model {
	t.Helper()
	m := New().SetSize(40, 20)
	m, _ = m.applyLoaded(folderChats())
	m, _ = m.SetFolders(folders)
	return m
}

// A folder made of category switches picks up chats that match, including
// ones that arrive after it was defined — which is the whole reason folders
// are rules rather than lists.
func TestFolder_NarrowsByCategory(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{{ID: 10, Title: "Groups", Groups: true}})
	m, _ = m.NextFolder()

	visible := m.visibleChats(m.chats)
	if len(visible) != 1 || visible[0].ID() != 2 {
		t.Fatalf("groups folder shows %+v, want only the supergroup", ids(visible))
	}
}

// Exclude beats everything, which is what the other clients do: a chat you
// removed from a folder stays removed even when it matches a category.
func TestFolder_ExcludeWinsOverCategory(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{
		{ID: 10, Title: "Groups", Groups: true, Exclude: []int64{2}},
	})
	m, _ = m.NextFolder()

	if visible := m.visibleChats(m.chats); len(visible) != 0 {
		t.Fatalf("excluded chat still shows: %v", ids(visible))
	}
}

func TestFolder_IncludeAddsAChatThatMatchesNoCategory(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{
		{ID: 10, Title: "Pinned", Include: []int64{3}},
	})
	m, _ = m.NextFolder()

	visible := m.visibleChats(m.chats)
	if len(visible) != 1 || visible[0].ID() != 3 {
		t.Fatalf("include-only folder shows %v, want chat 3", ids(visible))
	}
}

// A shared folder is exactly its list: the category switches do not exist on
// that variant, so treating their zero values as rules would add chats the
// person who shared it never put in.
func TestFolder_SharedFolderIsItsListAndNothingElse(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{
		{ID: 10, Title: "Shared", Include: []int64{1}, ExplicitOnly: true},
	})
	m, _ = m.NextFolder()

	visible := m.visibleChats(m.chats)
	if len(visible) != 1 || visible[0].ID() != 1 {
		t.Fatalf("shared folder shows %v, want only chat 1", ids(visible))
	}
}

func TestFolder_TabsCycleThroughTheUnfilteredList(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{
		{ID: 10, Title: "One", Groups: true},
		{ID: 11, Title: "Two", Broadcasts: true},
	})
	if _, ok := m.ActiveFolder(); ok {
		t.Fatal("a freshly installed folder set should start unfiltered")
	}
	m, _ = m.NextFolder()
	if f, _ := m.ActiveFolder(); f.ID != 10 {
		t.Fatalf("first next = %d, want folder 10", f.ID)
	}
	m, _ = m.NextFolder()
	if f, _ := m.ActiveFolder(); f.ID != 11 {
		t.Fatalf("second next = %d, want folder 11", f.ID)
	}
	m, _ = m.NextFolder()
	if _, ok := m.ActiveFolder(); ok {
		t.Fatal("cycling past the last folder should land on the unfiltered tab")
	}
	m, _ = m.PrevFolder()
	if f, _ := m.ActiveFolder(); f.ID != 11 {
		t.Fatalf("prev from unfiltered = %d, want the last folder", f.ID)
	}
}

// The tab the user is on has to survive a folder list being reinstalled —
// which happens on every reconnect. Dropping them back to "All" every time
// would make the feature unusable on a flaky link.
func TestSetFolders_KeepsTheActiveTabWhenItStillExists(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{
		{ID: 10, Title: "One", Groups: true},
		{ID: 11, Title: "Two", Broadcasts: true},
	})
	m, _ = m.NextFolder()
	m, _ = m.NextFolder()
	if f, _ := m.ActiveFolder(); f.ID != 11 {
		t.Fatalf("setup: active = %d", f.ID)
	}

	m, _ = m.SetFolders([]domain.Folder{
		{ID: 11, Title: "Two", Broadcasts: true},
		{ID: 10, Title: "One", Groups: true},
	})
	if f, _ := m.ActiveFolder(); f.ID != 11 {
		t.Fatalf("active folder after reinstall = %d, want 11 to survive", f.ID)
	}
}

// A folder deleted elsewhere must drop the user back to the full list rather
// than leaving them on an empty pane with no way to tell why.
func TestSetFolders_FallsBackWhenTheActiveFolderIsGone(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{{ID: 10, Title: "One", Groups: true}})
	m, _ = m.NextFolder()

	m, _ = m.SetFolders([]domain.Folder{{ID: 12, Title: "Other", Groups: true}})
	if _, ok := m.ActiveFolder(); ok {
		t.Fatal("a deleted folder should leave the list unfiltered")
	}
}

// An account with no folders should look exactly as it did before folders
// existed — no strip, and no row taken from the list.
func TestFolderStrip_IsEmptyWithoutFolders(t *testing.T) {
	t.Parallel()

	m := New().SetSize(40, 20)
	if strip := m.folderStrip(40); strip != "" {
		t.Fatalf("strip = %q, want nothing", strip)
	}
	if !strings.Contains(m.View(), "Chats") {
		t.Fatal("the header should still render")
	}
}

func TestFolderStrip_MarksTheActiveTab(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{{ID: 10, Title: "Work", Emoticon: "💼", Groups: true}})
	strip := ansi.Strip(m.folderStrip(60))
	if !strings.Contains(strip, "[All]") {
		t.Fatalf("strip = %q, want the active tab bracketed", strip)
	}
	if !strings.Contains(strip, "Work") {
		t.Fatalf("strip = %q, want the folder tab", strip)
	}
	// The emoji is part of the label the user chose on another client.
	if !strings.Contains(strip, "💼") {
		t.Fatalf("strip = %q, want the folder emoji", strip)
	}
}

func TestSelectFolder_IgnoresAPositionThatDoesNotExist(t *testing.T) {
	t.Parallel()

	m := modelWithFolders(t, []domain.Folder{{ID: 10, Title: "One", Groups: true}})
	m, _ = m.NextFolder()
	before, _ := m.ActiveFolder()

	m, _ = m.SelectFolder(7)
	after, ok := m.ActiveFolder()
	if !ok || after.ID != before.ID {
		t.Fatalf("out-of-range select moved the tab to %+v", after)
	}
}

func ids(items []ChatItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID()
	}
	return out
}
