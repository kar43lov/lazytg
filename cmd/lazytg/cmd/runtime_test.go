package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/ui/statusbar"
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

// TestStdinPrompter_CodePromptNamesDelivery pins that the prompt says where the
// code went. This was found during the first live login: the prompt said only
// "Code from Telegram:", the code had been delivered as a service message to an
// already-authorised app, and the natural assumption — an SMS that never came —
// cost several minutes and two extra SendCode calls.
func TestStdinPrompter_CodePromptNamesDelivery(t *testing.T) {
	cases := []struct {
		name string
		sent *tg.AuthSentCode
		want string
	}{
		{"nil stays bare", nil, "Code from Telegram: "},
		{
			"app",
			&tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}},
			"Code from Telegram (service message in the Telegram app on your other devices, not an SMS): ",
		},
		{
			"sms",
			&tg.AuthSentCode{Type: &tg.AuthSentCodeTypeSMS{Length: 5}},
			"Code from Telegram (SMS): ",
		},
		{
			"missed call carries prefix and length",
			&tg.AuthSentCode{Type: &tg.AuthSentCodeTypeMissedCall{Prefix: "+9989", Length: 6}},
			"Code from Telegram (missed call from +9989… — the code is the last 6 digits of the caller number): ",
		},
		{
			"email carries the pattern",
			&tg.AuthSentCode{Type: &tg.AuthSentCodeTypeEmailCode{EmailPattern: "p***@gmail.com"}},
			"Code from Telegram (email to p***@gmail.com): ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := strings.NewReader("12345\n")
			out := &bytes.Buffer{}
			p := newStdinPrompter(in, out, "")

			if _, err := p.Code(context.Background(), tc.sent); err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("prompt:\n got %q\nwant %q", got, tc.want)
			}
		})
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

// TestInitialConnState pins the status-bar seeding. The default from
// statusbar.New is "connecting", and no producer moves it in v0.1 — leaving a
// connected session permanently yellow. The mapping is trivial; the test exists
// so a future change to the state names cannot silently reintroduce that.
func TestInitialConnState(t *testing.T) {
	if got := initialConnState(true); got != statusbar.StateOnline {
		t.Errorf("attached: got %q, want %q", got, statusbar.StateOnline)
	}
	if got := initialConnState(false); got != statusbar.StateOffline {
		t.Errorf("not attached: got %q, want %q", got, statusbar.StateOffline)
	}
	if initialConnState(true) == statusbar.StateConnecting || initialConnState(false) == statusbar.StateConnecting {
		t.Error("seeded state must never be \"connecting\" — that is the value nothing ever clears")
	}
}
