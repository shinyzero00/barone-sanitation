package backend

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConsoleOTPProvider(t *testing.T) {
	var output bytes.Buffer
	provider := &ConsoleOTPProvider{Writer: &output}

	if err := provider.SendCode(t.Context(), " User@Example.COM ", "login", "123456"); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "OTP email=user@example.com purpose=login code=123456\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestConsoleOTPProviderRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, email, purpose, code string
	}{
		{"recipient", "user@example.com\nprivate", "login", "123456"},
		{"purpose", "user@example.com", "private", "123456"},
		{"code", "user@example.com", "login", "123456\nprivate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			provider := &ConsoleOTPProvider{Writer: &output}
			if err := provider.SendCode(t.Context(), tc.email, tc.purpose, tc.code); err == nil {
				t.Fatal("invalid input accepted")
			}
			if output.Len() != 0 {
				t.Fatalf("invalid input printed: %q", output.String())
			}
		})
	}
}

func TestConsoleOTPProviderErrors(t *testing.T) {
	if err := (&ConsoleOTPProvider{}).SendCode(t.Context(), "user@example.com", "login", "123456"); err == nil {
		t.Fatal("nil writer accepted")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output strings.Builder
	err := (&ConsoleOTPProvider{Writer: &output}).SendCode(ctx, "user@example.com", "login", "123456")
	if !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("cancelled send = %v, output %q", err, output.String())
	}
}
