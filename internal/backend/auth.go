package backend

import (
	"context"
	"crypto/hmac"
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
	Password string `json:"password" writeOnly:"true" doc:"12-128 bytes, validated by the service."`
	Phone    string `json:"phone"`
}

// CodeInput deliberately has no user ID or purpose override.
type CodeInput struct {
	Email string `json:"email"`
	Code  string `json:"code" pattern:"^[0-9]{6}$" writeOnly:"true"`
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

func validateCode(in CodeInput) (string, error) {
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return "", err
	}
	if len(in.Code) != 6 {
		return "", problem(422, "invalid_code")
	}
	for _, c := range in.Code {
		if c < '0' || c > '9' {
			return "", problem(422, "invalid_code")
		}
	}
	return email, nil
}

// RateLimit is shared by all instances and runs outside user transactions.
// HTTP callers should also invoke it before code verification endpoints.
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
	ID           int64        `db:"user_id"`
	Email        string       `db:"email"`
	PasswordHash string       `db:"password_hash"`
	Verified     sql.NullTime `db:"email_verified_at"`
}

func lockAuthUser(ctx context.Context, tx *sqlx.Tx, email string) (authUser, error) {
	var u authUser
	err := tx.GetContext(ctx, &u, `SELECT user_id,email,password_hash,email_verified_at FROM users WHERE lower(email)=$1 FOR UPDATE`, email)
	return u, err
}

type authCode struct {
	ID         int64        `db:"verification_code_id"`
	Email      string       `db:"email"`
	Hash       []byte       `db:"code_hash"`
	Challenge  []byte       `db:"challenge_hash"`
	Expires    time.Time    `db:"expires_at"`
	Used       sql.NullTime `db:"used_at"`
	Revoked    sql.NullTime `db:"revoked_at"`
	Attempts   int          `db:"failed_attempts"`
	Regenerate time.Time    `db:"regenerate_available_at"`
}

func latestAuthCode(ctx context.Context, tx *sqlx.Tx, userID int64, purpose string) (authCode, error) {
	var c authCode
	err := tx.GetContext(ctx, &c, `SELECT verification_code_id,email,code_hash,challenge_hash,expires_at,used_at,revoked_at,
		failed_attempts,regenerate_available_at FROM verification_codes WHERE user_id=$1 AND purpose=$2
		ORDER BY verification_code_id DESC LIMIT 1 FOR UPDATE`, userID, purpose)
	return c, err
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

type issuedCode struct {
	ID, UserID           int64
	Email, Purpose, Code string
}

// issueCode requires the user lock; the latest row is inspected even if no longer usable.
func (s *Service) issueCode(ctx context.Context, tx *sqlx.Tx, u authUser, purpose, challenge, ip string) (issuedCode, error) {
	var out issuedCode
	if len(s.Config.HMACSecret) < 32 || s.Config.CodeTTL <= 0 || s.Config.ResendInterval <= 0 || s.Config.MaxAttempts < 1 {
		return out, problem(503, "authentication_unavailable")
	}
	old, err := latestAuthCode(ctx, tx, u.ID, purpose)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	now, err := authNow(ctx, tx)
	if err != nil {
		return out, err
	}
	if old.ID != 0 && old.Regenerate.After(now) {
		return out, &APIError{Status: 429, Code: "code_cooldown", RetryAfter: old.Regenerate.Sub(now)}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE verification_codes SET revoked_at=$3 WHERE user_id=$1 AND purpose=$2 AND used_at IS NULL AND revoked_at IS NULL`, u.ID, purpose, now); err != nil {
		return out, err
	}
	code, err := newCode()
	if err != nil {
		return out, err
	}
	var challengeDigest []byte
	if purpose == "login" {
		challengeDigest = tokenHash(challenge)
	}
	var requestedIP any
	if ip != "" {
		requestedIP = ip
	}
	out = issuedCode{UserID: u.ID, Email: u.Email, Purpose: purpose, Code: code}
	// The hash includes the generated identity; the placeholder cannot escape this transaction.
	err = tx.QueryRowxContext(ctx, `INSERT INTO verification_codes
		(user_id,purpose,email,code_hash,challenge_hash,created_at,expires_at,regenerate_available_at,requested_ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING verification_code_id`,
		u.ID, purpose, u.Email, []byte{}, challengeDigest, now, now.Add(s.Config.CodeTTL), now.Add(s.Config.ResendInterval), requestedIP).Scan(&out.ID)
	if err != nil {
		return issuedCode{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE verification_codes SET code_hash=$2 WHERE verification_code_id=$1`, out.ID, codeHash(s.Config.HMACSecret, out.ID, code))
	return out, err
}

func (s *Service) deliverCode(ctx context.Context, c issuedCode) error {
	if c.ID == 0 {
		return nil
	}
	var err error
	if s.OTPProvider == nil {
		err = errors.New("OTP provider unavailable")
	} else {
		err = s.OTPProvider.SendCode(ctx, c.Email, c.Purpose, c.Code)
	}
	if err == nil {
		return nil
	}
	// Cleanup must survive a canceled HTTP request, but is still strictly bounded.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), max(s.Config.RequestTimeout, time.Second))
	defer cancel()
	revokeErr := s.authTx(cleanup, func(tx *sqlx.Tx) error {
		var id int64
		if err := tx.GetContext(cleanup, &id, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE`, c.UserID); err != nil {
			return err
		}
		var codeID int64
		if err := tx.GetContext(cleanup, &codeID, `SELECT verification_code_id FROM verification_codes WHERE verification_code_id=$1 AND user_id=$2 FOR UPDATE`, c.ID, c.UserID); err != nil {
			return err
		}
		_, err := tx.ExecContext(cleanup, `UPDATE verification_codes SET revoked_at=clock_timestamp()
			WHERE verification_code_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, codeID)
		return err
	})
	if revokeErr != nil {
		return errors.Join(problem(503, "mail_unavailable"), revokeErr)
	}
	return problem(503, "mail_unavailable")
}

// Register creates only a client. No code, password hash, or session is returned.
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
	var c issuedCode
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.QueryRowxContext(ctx, `INSERT INTO users(name,email,password_hash,role) VALUES ($1,$2,$3,'client') RETURNING user_id`, in.Name, email, hash).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO clients(user_id,phone) VALUES ($1,$2)`, id, in.Phone); err != nil {
			return err
		}
		u, err := lockAuthUser(ctx, tx, email)
		if err != nil {
			return err
		}
		c, err = s.issueCode(ctx, tx, u, "verify_email", "", ip)
		return err
	})
	if err != nil {
		return 0, err
	}
	if err = s.deliverCode(ctx, c); err != nil {
		return 0, err
	}
	return id, nil
}

// RequestCode allows only verification and password reset. Unknown emails succeed silently.
func (s *Service) RequestCode(ctx context.Context, email, purpose, ip string) error {
	email, err := canonicalEmail(email)
	if err != nil {
		return err
	}
	if purpose != "verify_email" && purpose != "reset_password" {
		return problem(422, "invalid_purpose")
	}
	if err = s.RateLimit(ctx, ip, "request_code"); err != nil {
		return err
	}
	var c issuedCode
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockAuthUser(ctx, tx, email)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if purpose == "verify_email" && u.Verified.Valid {
			return nil
		}
		c, err = s.issueCode(ctx, tx, u, purpose, "", ip)
		return err
	})
	if err != nil {
		return err
	}
	return s.deliverCode(ctx, c)
}

// StartLogin returns a challenge only for an HttpOnly, Secure, SameSite cookie,
// never for a response body. The HTTP layer must not log or expose it.
func (s *Service) StartLogin(ctx context.Context, email, password, ip string) (string, error) {
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
	var snapshot authUser
	err = s.DB.GetContext(ctx, &snapshot, `SELECT user_id,email,password_hash,email_verified_at FROM users WHERE lower(email)=$1`, email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", problem(401, "invalid_credentials")
	}
	if err != nil {
		return "", err
	}
	if !checkPassword(snapshot.PasswordHash, password) {
		return "", problem(401, "invalid_credentials")
	}
	challenge, err := newToken()
	if err != nil {
		return "", err
	}
	var c issuedCode
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockAuthUser(ctx, tx, email)
		if err != nil {
			return err
		}
		if u.ID != snapshot.ID || u.PasswordHash != snapshot.PasswordHash || !u.Verified.Valid {
			return problem(401, "invalid_credentials")
		}
		c, err = s.issueCode(ctx, tx, u, "login", challenge, ip)
		return err
	})
	if err != nil {
		return "", err
	}
	if err = s.deliverCode(ctx, c); err != nil {
		return "", err
	}
	return challenge, nil
}

// verifyCode returns denial separately so failed-attempt writes can be committed.
// It requires the user lock and never falls back to an older code.
func (s *Service) verifyCode(ctx context.Context, tx *sqlx.Tx, u authUser, in CodeInput, purpose, challenge string) (denial error, err error) {
	c, err := latestAuthCode(ctx, tx, u.ID, purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return problem(401, "invalid_code"), nil
	}
	if err != nil {
		return nil, err
	}
	now, err := authNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	if c.Attempts >= s.Config.MaxAttempts {
		return &APIError{Status: 429, Code: "code_locked", RetryAfter: max(time.Second, c.Regenerate.Sub(now))}, nil
	}
	if c.Used.Valid || c.Revoked.Valid || !c.Expires.After(now) {
		return problem(401, "invalid_code"), nil
	}
	match := c.Email == u.Email && hmac.Equal(c.Hash, codeHash(s.Config.HMACSecret, c.ID, in.Code))
	if purpose == "login" {
		match = match && validToken(challenge) && hmac.Equal(c.Challenge, tokenHash(challenge)) && u.Verified.Valid
	}
	if !match {
		c.Attempts++
		if c.Attempts >= s.Config.MaxAttempts {
			c.Regenerate = maxTime(c.Regenerate, now.Add(max(15*time.Minute, s.Config.Lockout)))
		}
		_, err = tx.ExecContext(ctx, `UPDATE verification_codes SET failed_attempts=$2,regenerate_available_at=$3 WHERE verification_code_id=$1`, c.ID, c.Attempts, c.Regenerate)
		if err != nil {
			return nil, err
		}
		if c.Attempts >= s.Config.MaxAttempts {
			return &APIError{Status: 429, Code: "code_locked", RetryAfter: c.Regenerate.Sub(now)}, nil
		}
		return problem(401, "invalid_code"), nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE verification_codes SET used_at=$2 WHERE verification_code_id=$1`, c.ID, now)
	return nil, err
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s *Service) completeCode(ctx context.Context, in CodeInput, purpose, challenge string, apply func(*sqlx.Tx, authUser) error) error {
	email, err := validateCode(in)
	if err != nil {
		return err
	}
	var denial error
	err = s.authTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockAuthUser(ctx, tx, email)
		if errors.Is(err, sql.ErrNoRows) {
			denial = problem(401, "invalid_code")
			return nil
		}
		if err != nil {
			return err
		}
		denial, err = s.verifyCode(ctx, tx, u, in, purpose, challenge)
		if err != nil {
			return err
		}
		if denial != nil {
			return nil
		}
		return apply(tx, u)
	})
	if err != nil {
		return err
	}
	return denial
}

func (s *Service) VerifyEmail(ctx context.Context, in CodeInput) error {
	return s.completeCode(ctx, in, "verify_email", "", func(tx *sqlx.Tx, u authUser) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET email_verified_at=COALESCE(email_verified_at,clock_timestamp()) WHERE user_id=$1`, u.ID)
		return err
	})
}

// FinishLogin requires the challenge read from the secure login cookie.
// The returned opaque session token belongs only in a secure HTTP cookie.
func (s *Service) FinishLogin(ctx context.Context, in CodeInput, challenge string) (string, error) {
	if s.Config.SessionTTL <= 0 {
		return "", problem(503, "authentication_unavailable")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.completeCode(ctx, in, "login", challenge, func(tx *sqlx.Tx, u authUser) error {
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

func (s *Service) ResetPassword(ctx context.Context, in CodeInput, newPassword string) error {
	if !validPassword(newPassword) {
		return problem(422, "invalid_password")
	}
	if _, err := validateCode(in); err != nil {
		return err
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.completeCode(ctx, in, "reset_password", "", func(tx *sqlx.Tx, u authUser) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=$2 WHERE user_id=$1`, u.ID, hash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL`, u.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE verification_codes SET revoked_at=clock_timestamp() WHERE user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, u.ID)
		return err
	})
}

func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	var actor Actor
	if !validToken(token) {
		return actor, problem(401, "unauthenticated")
	}
	err := s.DB.GetContext(ctx, &actor, `SELECT u.user_id,u.role,s.session_id FROM sessions s JOIN users u USING(user_id)
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND u.email_verified_at IS NOT NULL`, tokenHash(token))
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
