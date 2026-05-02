package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestStdinPrompter_MultiLineBufferedInput exercises piped input where the
// caller feeds phone+code on consecutive lines via a single Reader. A previous
// implementation built bufio.NewReader inside readLine on every call: bufio
// reads ahead past the first newline, so the second readLine would see EOF
// even though the second line had been written into the underlying buffer.
// Reusing one bufio.Reader across calls is what makes scripted login work.
func TestStdinPrompter_MultiLineBufferedInput(t *testing.T) {
	in := strings.NewReader("+79991112233\n12345\n")
	out := &bytes.Buffer{}
	p := newStdinPrompter(in, out, "")

	phone, err := p.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if phone != "+79991112233" {
		t.Fatalf("phone: got %q, want %q", phone, "+79991112233")
	}

	code, err := p.Code(context.Background(), nil)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if code != "12345" {
		t.Fatalf("code: got %q, want %q", code, "12345")
	}
}

// TestStdinPrompter_PresetPhoneSkipsRead ensures the preset phone short-circuit
// does not consume bytes from the reader, leaving them available for Code.
func TestStdinPrompter_PresetPhoneSkipsRead(t *testing.T) {
	in := strings.NewReader("12345\n")
	out := &bytes.Buffer{}
	p := newStdinPrompter(in, out, "+79991112233")

	phone, err := p.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if phone != "+79991112233" {
		t.Fatalf("phone: got %q, want %q", phone, "+79991112233")
	}

	code, err := p.Code(context.Background(), nil)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if code != "12345" {
		t.Fatalf("code: got %q, want %q", code, "12345")
	}
}

// TestStdinPrompter_EmptyLineErrors confirms blank input is reported so the
// caller can decide whether to retry.
func TestStdinPrompter_EmptyLineErrors(t *testing.T) {
	in := strings.NewReader("\n")
	out := &bytes.Buffer{}
	p := newStdinPrompter(in, out, "")

	if _, err := p.Phone(context.Background()); err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
}
