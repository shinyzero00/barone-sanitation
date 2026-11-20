package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

type diagnostic struct {
	message string
	cause   error
}

func (d diagnostic) Error() string { return d.message }
func (d diagnostic) Unwrap() error { return d.cause }

// Driver and migration errors can contain credentials, SQL, or submitted values.
func commandFailure(step, reason string, err error) error {
	if err == nil {
		return nil
	}
	if partial, ok := errors.AsType[*goose.PartialError](err); ok && partial.Failed != nil && partial.Failed.Source != nil {
		step = fmt.Sprintf("%s (version %d)", step, partial.Failed.Source.Version)
	}
	switch {
	case errors.Is(err, context.Canceled):
		reason = "operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "operation timed out"
	case errors.Is(err, syscall.EADDRINUSE):
		reason = "LISTEN_ADDRESS is already in use"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		reason = "LISTEN_ADDRESS is not available on this host"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "PostgreSQL connection refused; check server availability and DATABASE_URL"
	case errors.Is(err, goose.ErrNoNextVersion):
		reason = "no migration to roll back"
	case errors.Is(err, goose.ErrVersionNotFound):
		reason = "database references a migration version missing from this build"
	default:
		if _, ok := errors.AsType[*pgconn.ParseConfigError](err); ok {
			reason = "invalid DATABASE_URL or PostgreSQL connection settings"
		} else if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
			switch pg.Code {
			case "28000", "28P01":
				reason = "PostgreSQL rejected authentication"
			case "3D000":
				reason = "PostgreSQL database does not exist; check DATABASE_URL"
			case "42501":
				reason = "PostgreSQL permission denied"
			case "42P01":
				reason = "PostgreSQL table is missing; check migration state"
			case "42703":
				reason = "PostgreSQL column is missing; check pending migrations and schema history"
			case "42701":
				reason = "PostgreSQL column already exists; check schema against migration history"
			default:
				reason = "PostgreSQL reported an error"
			}
			if validSQLState(pg.Code) {
				reason += " (SQLSTATE " + pg.Code + ")"
			}
		} else if dns, ok := errors.AsType[*net.DNSError](err); ok && dns.IsNotFound {
			reason = "host not found; check the configured hostname"
		} else if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
			reason = "network operation timed out"
		}
	}
	return diagnostic{message: step + ": " + reason, cause: err}
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
