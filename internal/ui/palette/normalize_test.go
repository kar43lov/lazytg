package palette

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// "Алёна" decomposes to "Але" + combining diaeresis + "на".
		// Drop Mn → "Алена"; ToLower → "алена". This is the headline
		// invariant: typing "Алена" must match a chat titled "Алёна".
		{"russian_yo_to_e", "Алёна", "алена"},

		// French composed character: "é" → "e" + ́ → "e".
		{"french_acute", "Café", "cafe"},

		// French diaeresis: "ï" → "i" + ̈ → "i".
		{"french_diaeresis", "naïve", "naive"},

		// Plain ASCII: only the case fold should kick in.
		{"ascii_lowercase", "Hello", "hello"},

		// Already-lowercase ASCII passes straight through.
		{"ascii_unchanged", "hello world", "hello world"},

		// Emoji is not Mn-category and lowercase-of-emoji is the same
		// rune; the surrounding text is folded as usual.
		{"emoji_kept", "🚀 Boom", "🚀 boom"},

		// Empty string is a separate code path (early return) — keep
		// it covered so a future refactor can't sneak a panic in.
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Normalize(c.in)
			if got != c.want {
				t.Fatalf("Normalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestNormalize_AlyonaRoundTrip is the user-facing invariant the
// palette exists to deliver: a Russian chat titled "Алёна" must be
// findable by typing the alternate spelling "Алена" (and vice versa).
// Express it as an explicit equality check so the assertion message
// reads as documentation in CI logs.
func TestNormalize_AlyonaRoundTrip(t *testing.T) {
	if Normalize("Алёна") != Normalize("Алена") {
		t.Fatalf("Normalize must equate \"Алёна\" and \"Алена\" (got %q vs %q)",
			Normalize("Алёна"), Normalize("Алена"))
	}
}
