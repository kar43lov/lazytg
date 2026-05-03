package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello.pdf", "hello.pdf"},
		{"  hello.pdf  ", "hello.pdf"},
		{"a/b\\c.txt", "a_b_c.txt"},
		{"..", ""},
		{".", ""},
		{"", ""},
		{"foo:bar?baz", "foo_bar_baz"},
		{"with\x00null", "with_null"},
		{"  spaced  name  ", "spaced  name"},
		{"русский файл.pdf", "русский файл.pdf"},
		{"emoji 📎.png", "emoji 📎.png"},
		{".hidden", "hidden"},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFileStore_PathAndEnsureDir(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !strings.HasPrefix(store.Root(), "/") {
		t.Fatalf("Root must be absolute: %q", store.Root())
	}

	p := store.Path("Family Group", "vacation.jpg")
	want := filepath.Join(store.Root(), "Family Group", "vacation.jpg")
	if p != want {
		t.Fatalf("Path = %q, want %q", p, want)
	}

	// Slashes in chat title MUST be sanitised so a malicious or
	// awkward title cannot escape the root.
	dangerous := store.Path("../etc", "passwd")
	if !strings.HasPrefix(dangerous, store.Root()) {
		t.Fatalf("dangerous path escapes root: %q", dangerous)
	}

	// EnsureDir creates the parent at 0700.
	if err := store.EnsureDir(p); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	dir := filepath.Dir(p)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != dirPerm {
		t.Fatalf("dir perm: got %o want %o", info.Mode().Perm(), dirPerm)
	}

	// Empty filename → fallback "file".
	fallback := store.Path("Family", "")
	if filepath.Base(fallback) != "file" {
		t.Fatalf("expected fallback 'file', got %q", filepath.Base(fallback))
	}
}

func TestFileStore_Exists(t *testing.T) {
	root := t.TempDir()
	store, _ := NewFileStore(root, nil)
	missing := filepath.Join(root, "nope")
	ok, err := store.Exists(missing)
	if err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	exist := filepath.Join(root, "real")
	if err := os.WriteFile(exist, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ok, err = store.Exists(exist)
	if err != nil || !ok {
		t.Fatalf("real: ok=%v err=%v", ok, err)
	}
	// Directory path must not be reported as a regular file.
	ok, _ = store.Exists(root)
	if ok {
		t.Fatalf("dir reported as file")
	}
}
