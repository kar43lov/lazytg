package tg

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// CodePrompter abstracts how lazytg asks the human for the bits gotd needs to
// complete an authentication flow: phone number, SMS code, and 2FA password.
//
// The CLI implements it against stdin; the TUI (stage 2) will implement it as
// a modal overlay. Tests inject a deterministic mock.
type CodePrompter interface {
	Phone(ctx context.Context) (string, error)
	Code(ctx context.Context, sentTo *tg.AuthSentCode) (string, error)
	Password(ctx context.Context) (string, error)
}

// ErrSignUpRequired signals that the phone is not registered with Telegram.
// lazytg deliberately refuses to sign up new users — the threat model
// document explains why (we are an unofficial client, signups make us look
// like spam).
var ErrSignUpRequired = errors.New("sign-up required, lazytg only supports existing accounts")

// authAdapter wires a CodePrompter into the auth.UserAuthenticator interface
// that gotd's Flow expects. Sign-up is rejected.
type authAdapter struct {
	phone    string
	prompter CodePrompter
}

func (a *authAdapter) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	return a.prompter.Phone(ctx)
}

func (a *authAdapter) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	return a.prompter.Code(ctx, sentCode)
}

func (a *authAdapter) Password(ctx context.Context) (string, error) {
	return a.prompter.Password(ctx)
}

func (a *authAdapter) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return ErrSignUpRequired
}

func (a *authAdapter) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, ErrSignUpRequired
}

// Login runs the gotd auth flow against client using prompter to ask the
// human for phone/code/password. If phone is non-empty it is used verbatim
// and prompter.Phone is never called — handy for `lazytg login --account +7…`.
//
// Behaviour matches the gotd "auth" example with the explicit no-sign-up
// guard above.
func Login(ctx context.Context, client *telegram.Client, phone string, prompter CodePrompter) error {
	if prompter == nil {
		return errors.New("login: prompter is required")
	}
	flow := auth.NewFlow(
		&authAdapter{phone: phone, prompter: prompter},
		auth.SendCodeOptions{},
	)
	if err := client.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("auth flow: %w", err)
	}
	return nil
}
