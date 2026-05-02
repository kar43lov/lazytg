package domain_test

import (
	"errors"
	"testing"

	"github.com/pgmac/lazytg/internal/core/domain"
)

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "canonical", in: "+79991112233", want: "+79991112233"},
		{name: "spaces", in: "+7 999 111 22 33", want: "+79991112233"},
		{name: "dashes", in: "+7-999-111-22-33", want: "+79991112233"},
		{name: "parens and spaces", in: "+7 (999) 111-22-33", want: "+79991112233"},
		{name: "dots", in: "+7.999.111.22.33", want: "+79991112233"},
		{name: "tabs and trailing whitespace", in: "  +7\t999\t111\t22\t33  ", want: "+79991112233"},
		{name: "min length 10", in: "+1234567890", want: "+1234567890"},
		{name: "max length 15", in: "+123456789012345", want: "+123456789012345"},

		{name: "missing plus", in: "79991112233", wantErr: true},
		{name: "letters", in: "+7999abc1112", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "only plus", in: "+", wantErr: true},
		{name: "too short", in: "+123456789", wantErr: true},
		{name: "too long", in: "+1234567890123456", wantErr: true},
		{name: "plus not first", in: "7+9991112233", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NormalizePhone(tc.in)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalidPhone) {
					t.Fatalf("got err=%v, want ErrInvalidPhone", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
