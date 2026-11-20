package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

func TestCommandFailure(t *testing.T) {
	t.Parallel()
	const secret = "do-not-log-this"
	permission := &pgconn.PgError{Code: "42501", Message: secret, Detail: secret}
	for _, tc := range []struct {
		name, step, want string
		err              error
	}{
		{"listen", "serve", "serve: LISTEN_ADDRESS is already in use", syscall.EADDRINUSE},
		{"refused", "database connection", "database connection: PostgreSQL connection refused; check server availability and DATABASE_URL", syscall.ECONNREFUSED},
		{"timeout", "migrate up", "migrate up: operation timed out", context.DeadlineExceeded},
		{"authentication", "database connection", "database connection: PostgreSQL rejected authentication (SQLSTATE 28P01)", &pgconn.PgError{Code: "28P01", Message: secret}},
		{"migration", "migrate up", "migrate up (version 7): PostgreSQL permission denied (SQLSTATE 42501)", &goose.PartialError{
			Failed: &goose.MigrationResult{Source: &goose.Source{Version: 7, Path: secret}}, Err: permission,
		}},
		{"unknown", "admin", "admin: fallback", errors.New(secret)},
		{"missing_column", "seed demo", "seed demo: PostgreSQL column is missing; check pending migrations and schema history (SQLSTATE 42703)", &pgconn.PgError{Code: "42703", Message: secret}},
		{"duplicate_column", "migrate up", "migrate up: PostgreSQL column already exists; check schema against migration history (SQLSTATE 42701)", &pgconn.PgError{Code: "42701", Message: secret}},
		{"invalid_sqlstate", "migrate up", "migrate up: PostgreSQL reported an error", &pgconn.PgError{Code: "bad\n!", Message: secret}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Errorf("%s: %w", secret, tc.err)
			got := commandFailure(tc.step, "fallback", raw)
			if got.Error() != tc.want {
				t.Fatalf("unexpected diagnostic, want %q", tc.want)
			}
			if !errors.Is(got, tc.err) {
				t.Fatal("cause was not preserved")
			}
		})
	}
}

func TestRunConfigurationDiagnostics(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run(nil); err == nil || err.Error() != "configuration: DATABASE_URL is required" {
		t.Fatal("expected missing DATABASE_URL diagnostic")
	}
	t.Setenv("DATABASE_URL", "postgres://user:do-not-log-this@localhost:invalid/test")
	t.Setenv("OTP_HMAC_SECRET", "")
	if err := run(nil); err == nil || !strings.HasPrefix(err.Error(), "configuration: OTP_HMAC_SECRET must be base64") {
		t.Fatal("expected invalid OTP_HMAC_SECRET diagnostic")
	}
	if err := run([]string{"migrate", "up"}); err == nil || err.Error() != "database connection: invalid DATABASE_URL or PostgreSQL connection settings" {
		t.Fatal("expected safe invalid DATABASE_URL diagnostic")
	}
}
