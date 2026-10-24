package backend

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

type RegisterInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Phone    string `json:"phone"`
}

func canonicalEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n") || !strings.Contains(email, "@") {
		return "", problem(422, "invalid_email")
	}
	return email, nil
}

func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 128 }

func validText(s string, max int) bool {
	return utf8.ValidString(s) && len(s) > 0 && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

// RateLimit is shared by all instances and runs outside user transactions.
func (s *Service) RateLimit(ctx context.Context, ip, bucket string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || !validText(bucket, 100) {
		return problem(422, "invalid_rate_limit_key")
	}
	if s.Config.RateLimit < 1 || s.Config.RateWindow <= 0 {
		return problem(503, "rate_limiter_unavailable")
	}
	var count int
	var remaining float64
	err := s.DB.QueryRowxContext(ctx, `INSERT INTO ip_rate_limits (requested_ip,bucket,window_started_at,request_count)
		VALUES ($1,$2,statement_timestamp(),1)
		ON CONFLICT (requested_ip,bucket) DO UPDATE SET
		request_count = CASE WHEN ip_rate_limits.window_started_at + $3 * interval '1 second' <= statement_timestamp()
		THEN 1 ELSE LEAST(ip_rate_limits.request_count::bigint + 1, $4::bigint + 1)::integer END,
		window_started_at = CASE WHEN ip_rate_limits.window_started_at + $3 * interval '1 second' <= statement_timestamp()
		THEN statement_timestamp() ELSE ip_rate_limits.window_started_at END
		RETURNING request_count, GREATEST(0, EXTRACT(EPOCH FROM (window_started_at + $3 * interval '1 second' - statement_timestamp())))`,
		parsed.String(), bucket, s.Config.RateWindow.Seconds(), s.Config.RateLimit).Scan(&count, &remaining)
	if err != nil {
		return err
	}
	if count > s.Config.RateLimit {
		return &APIError{Status: 429, Code: "rate_limited", RetryAfter: max(time.Second, time.Duration(remaining*float64(time.Second)))}
	}
	return nil
}

type authUser struct {
	ID           int64  `db:"user_id"`
	Email        string `db:"email"`
	PasswordHash string `db:"password_hash"`
}

func lockAuthUser(ctx context.Context, tx *sqlx.Tx, email string) (authUser, error) {
	var u authUser
	err := tx.GetContext(ctx, &u, `SELECT user_id,email,password_hash FROM users WHERE lower(email)=$1 FOR UPDATE`, email)
	return u, err
}

func authNow(ctx context.Context, tx *sqlx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.GetContext(ctx, &now, `SELECT clock_timestamp()`)
	return now, err
}

func (s *Service) authTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return databaseError(err)
	}
	return tx.Commit()
}

// Register creates only a client. No password hash or session is returned.
func (s *Service) Register(ctx context.Context, in RegisterInput, ip string) (int64, error) {
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return 0, err
	}
	in.Name, in.Phone = strings.TrimSpace(in.Name), strings.TrimSpace(in.Phone)
	if !validText(in.Name, 200) || !validText(in.Phone, 40) || !validPassword(in.Password) {
		return 0, problem(422, "invalid_registration")
	}
	for _, r := range in.Phone {
		if !strings.ContainsRune("0123456789+() -.x", r) {
			return 0, problem(422, "invalid_phone")
		}
	}
	if !strings.ContainsAny(in.Phone, "0123456789") {
		return 0, problem(422, "invalid_phone")
	}
	if err = s.RateLimit(ctx, ip, "register"); err != nil {
		return 0, err
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.QueryRowxContext(ctx, `INSERT INTO users(name,email,password_hash,role) VALUES ($1,$2,$3,'client') RETURNING user_id`, in.Name, email, hash).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO clients(user_id,phone) VALUES ($1,$2)`, id, in.Phone)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Login returns an opaque session token only for a secure HTTP cookie.
func (s *Service) Login(ctx context.Context, email, password, ip string) (string, error) {
	email, err := canonicalEmail(email)
	if err != nil {
		return "", err
	}
	if !validPassword(password) {
		return "", problem(401, "invalid_credentials")
	}
	if err = s.RateLimit(ctx, ip, "login"); err != nil {
		return "", err
	}
	if s.Config.SessionTTL <= 0 {
		return "", problem(503, "authentication_unavailable")
	}
	var snapshot authUser
	err = s.DB.GetContext(ctx, &snapshot, `SELECT user_id,email,password_hash FROM users WHERE lower(email)=$1`, email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", problem(401, "invalid_credentials")
	}
	if err != nil {
		return "", err
	}
	if !checkPassword(snapshot.PasswordHash, password) {
		return "", problem(401, "invalid_credentials")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockAuthUser(ctx, tx, email)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(401, "invalid_credentials")
		}
		if err != nil {
			return err
		}
		// Password work runs outside the lock; reject a changed identity or hash.
		if u.ID != snapshot.ID || u.PasswordHash != snapshot.PasswordHash {
			return problem(401, "invalid_credentials")
		}
		now, err := authNow(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions(user_id,token_hash,created_at,expires_at) VALUES ($1,$2,$3,$4)`, u.ID, tokenHash(token), now, now.Add(s.Config.SessionTTL))
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	var actor Actor
	if !validToken(token) {
		return actor, problem(401, "unauthenticated")
	}
	err := s.DB.GetContext(ctx, &actor, `SELECT u.user_id,u.role,s.session_id FROM sessions s JOIN users u USING(user_id)
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`, tokenHash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, problem(401, "unauthenticated")
	}
	return actor, err
}

func (s *Service) Logout(ctx context.Context, actor Actor) error {
	if actor.UserID <= 0 || actor.SessionID <= 0 {
		return problem(401, "unauthenticated")
	}
	return s.authTx(ctx, func(tx *sqlx.Tx) error {
		var id int64
		if err := tx.GetContext(ctx, &id, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE`, actor.UserID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE session_id=$1 AND user_id=$2`, actor.SessionID, actor.UserID)
		return err
	})
}

type Profile struct {
	UserID int64  `db:"user_id" json:"user_id"`
	Name   string `db:"name" json:"name"`
	Email  string `db:"email" json:"email"`
	Role   string `db:"role" json:"role"`
	Phone  string `db:"phone" json:"phone"`
}

func (s *Service) GetProfile(ctx context.Context, actor Actor) (Profile, error) {
	var out Profile
	if actor.UserID <= 0 {
		return out, problem(401, "unauthenticated")
	}
	err := s.DB.GetContext(ctx, &out, `SELECT u.user_id,u.name,u.email,u.role,COALESCE(c.phone,'') AS phone
		FROM users u LEFT JOIN clients c USING(user_id) WHERE u.user_id=$1`, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, problem(404, "not_found")
	}
	return out, err
}
