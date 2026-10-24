package backend

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type Service struct {
	DB     *sqlx.DB
	Config Config
}

type Actor struct {
	UserID    int64  `db:"user_id" json:"user_id"`
	Role      string `db:"role" json:"role"`
	SessionID int64  `db:"session_id" json:"-"`
}

type APIError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return e.Code }

func problem(status int, code string) error { return &APIError{Status: status, Code: code} }

func databaseError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return problem(409, "conflict")
		case "23503", "23514", "22003", "22007", "22P02":
			return problem(422, "invalid_reference_or_value")
		}
	}
	return err
}
