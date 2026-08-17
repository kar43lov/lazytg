package tg

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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
		return fmt.Errorf("auth flow: %w", ExplainCredentialError(err))
	}
	return nil
}

// ExplainCredentialError rewrites the two api_id-level rejections Telegram
// returns into something a user can act on. Both arrive as opaque RPC codes
// that mean nothing outside the MTProto world, and both have the same fix
// (bring your own api_id), so the remediation is attached here rather than
// left for every call site to reinvent.
//
// API_ID_PUBLISHED_FLOOD means the api_id has been seen in public source and
// is now refused for end-user logins — if it fires against a release build it
// means the shipped key burned and every user is affected at once.
// API_ID_INVALID means the id/hash pair does not exist or does not match.
//
// Errors that are not credential-related pass through untouched. The original
// RPC error stays wrapped in every branch, so retry policies that match on the
// code keep working.
//
//nolint:staticcheck // ST1005: multi-line user-facing remediation text
func ExplainCredentialError(err error) error {
	switch {
	case err == nil:
		return nil
	case tgerr.Is(err, "API_ID_PUBLISHED_FLOOD"):
		return fmt.Errorf("%w\n"+
			"  Telegram refuses logins with this api_id because it was found in\n"+
			"  public source. Supply your own credentials from\n"+
			"  https://my.telegram.org/apps:\n"+
			"    export %s=1234567\n"+
			"    export %s=<32-hex api_hash>\n"+
			"  Run `lazytg version` to see which credential source is active.",
			err, EnvAPIID, EnvAPIHash)
	case tgerr.Is(err, "API_ID_INVALID"):
		return fmt.Errorf("%w\n"+
			"  The api_id/api_hash pair is not valid. They must come from the\n"+
			"  same application at https://my.telegram.org/apps — a hash from\n"+
			"  one app with the id of another is rejected.\n"+
			"  Run `lazytg version` to see which credential source is active.",
			err)
	default:
		return err
	}
}
