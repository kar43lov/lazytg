package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// mockPrompter records what was asked of it and returns scripted answers.
type mockPrompter struct {
	phone       string
	phoneErr    error
	code        string
	codeErr     error
	password    string
	passwordErr error

	phoneCalls    int
	codeCalls     int
	passwordCalls int
}

func (m *mockPrompter) Phone(_ context.Context) (string, error) {
	m.phoneCalls++
	return m.phone, m.phoneErr
}

func (m *mockPrompter) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	m.codeCalls++
	return m.code, m.codeErr
}

func (m *mockPrompter) Password(_ context.Context) (string, error) {
	m.passwordCalls++
	return m.password, m.passwordErr
}

func TestAuthAdapter_PhoneFromConstructorBypassesPrompter(t *testing.T) {
	t.Parallel()
	p := &mockPrompter{phone: "should-not-be-called"}
	a := &authAdapter{phone: "+79991112233", prompter: p}

	got, err := a.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if got != "+79991112233" {
		t.Fatalf("Phone: want preset value, got %q", got)
	}
	if p.phoneCalls != 0 {
		t.Fatalf("Phone: prompter must not be called when phone is preset, got %d calls", p.phoneCalls)
	}
}

func TestAuthAdapter_PhoneFallsBackToPrompter(t *testing.T) {
	t.Parallel()
	p := &mockPrompter{phone: "+71112223344"}
	a := &authAdapter{prompter: p}

	got, err := a.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if got != "+71112223344" {
		t.Fatalf("Phone: want %q, got %q", "+71112223344", got)
	}
	if p.phoneCalls != 1 {
		t.Fatalf("Phone: prompter should be called once, got %d", p.phoneCalls)
	}
}

func TestAuthAdapter_DelegatesCodeAndPassword(t *testing.T) {
	t.Parallel()
	p := &mockPrompter{code: "12345", password: "hunter2"}
	a := &authAdapter{prompter: p}

	code, err := a.Code(context.Background(), &tg.AuthSentCode{})
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if code != "12345" {
		t.Fatalf("Code: want %q, got %q", "12345", code)
	}

	pw, err := a.Password(context.Background())
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if pw != "hunter2" {
		t.Fatalf("Password: want %q, got %q", "hunter2", pw)
	}
}

func TestAuthAdapter_RejectsSignUp(t *testing.T) {
	t.Parallel()
	a := &authAdapter{prompter: &mockPrompter{}}

	if _, err := a.SignUp(context.Background()); !errors.Is(err, ErrSignUpRequired) {
		t.Fatalf("SignUp: want ErrSignUpRequired, got %v", err)
	}
	if err := a.AcceptTermsOfService(context.Background(), tg.HelpTermsOfService{}); !errors.Is(err, ErrSignUpRequired) {
		t.Fatalf("AcceptTermsOfService: want ErrSignUpRequired, got %v", err)
	}
}

func TestAuthAdapter_PropagatesPrompterErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("user cancelled")
	a := &authAdapter{prompter: &mockPrompter{phoneErr: want, codeErr: want, passwordErr: want}}

	if _, err := a.Phone(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Phone error: want %v, got %v", want, err)
	}
	if _, err := a.Code(context.Background(), &tg.AuthSentCode{}); !errors.Is(err, want) {
		t.Fatalf("Code error: want %v, got %v", want, err)
	}
	if _, err := a.Password(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Password error: want %v, got %v", want, err)
	}
}

func TestLogin_RequiresPrompter(t *testing.T) {
	t.Parallel()
	if err := Login(context.Background(), nil, "+7000", nil); err == nil {
		t.Fatalf("Login(nil prompter) must fail")
	}
}

// mockFlowClient drives auth.Flow.Run end-to-end without touching MTProto.
// We replay the same scenario as gotd's e2etest fixtures: SendCode returns a
// fake AuthSentCode, the first SignIn answers with ErrPasswordAuthNeeded so
// we exercise the 2FA path, and Password yields a successful authorization.
type mockFlowClient struct {
	signInCalls   int
	passwordCalls int
	signUpCalls   int
}

func (m *mockFlowClient) SendCode(_ context.Context, _ string, _ auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	return &tg.AuthSentCode{
		PhoneCodeHash: "hash",
		Type:          &tg.AuthSentCodeTypeApp{Length: 5},
		Timeout:       10,
	}, nil
}

func (m *mockFlowClient) SignIn(_ context.Context, _ string, _ string, _ string) (*tg.AuthAuthorization, error) {
	m.signInCalls++
	return nil, auth.ErrPasswordAuthNeeded
}

func (m *mockFlowClient) Password(_ context.Context, _ string) (*tg.AuthAuthorization, error) {
	m.passwordCalls++
	return &tg.AuthAuthorization{User: &tg.User{ID: 42, Username: "tester"}}, nil
}

func (m *mockFlowClient) SignUp(_ context.Context, _ auth.SignUp) (*tg.AuthAuthorization, error) {
	m.signUpCalls++
	return nil, errors.New("SignUp must not be called")
}

func TestAuthAdapter_DrivesFlowEndToEnd(t *testing.T) {
	t.Parallel()
	prompter := &mockPrompter{code: "12345", password: "hunter2"}
	flow := auth.NewFlow(
		&authAdapter{phone: "+79991112233", prompter: prompter},
		auth.SendCodeOptions{},
	)

	client := &mockFlowClient{}
	if err := flow.Run(context.Background(), client); err != nil {
		t.Fatalf("Flow.Run: %v", err)
	}
	if client.signInCalls != 1 {
		t.Fatalf("SignIn calls = %d, want 1", client.signInCalls)
	}
	if client.passwordCalls != 1 {
		t.Fatalf("Password calls = %d, want 1", client.passwordCalls)
	}
	if client.signUpCalls != 0 {
		t.Fatalf("SignUp calls = %d, want 0 (lazytg never signs up)", client.signUpCalls)
	}
	if prompter.codeCalls != 1 {
		t.Fatalf("prompter.Code calls = %d, want 1", prompter.codeCalls)
	}
	if prompter.passwordCalls != 1 {
		t.Fatalf("prompter.Password calls = %d, want 1", prompter.passwordCalls)
	}
	if prompter.phoneCalls != 0 {
		t.Fatalf("prompter.Phone calls = %d, want 0 (phone was preset)", prompter.phoneCalls)
	}
}
