//go:build integration

package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

type authRecordingMailer struct {
	mu    sync.Mutex
	codes map[string]string
	fail  bool
	hook  func()
}

func (m *authRecordingMailer) SendCode(_ context.Context, email, _, code string) error {
	m.mu.Lock()
	m.codes[email] = code
	fail, hook := m.fail, m.hook
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return errors.New("test SMTP failure")
	}
	return nil
}

func (m *authRecordingMailer) Code(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[email]
}

func authFixture(t *testing.T) (*Service, *authRecordingMailer) {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	admin := sqlx.NewDb(stdlib.OpenDB(*config), "pgx")
	t.Cleanup(func() { admin.Close() })
	name, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_" + fmt.Sprintf("%x", tokenHash(name)[:12])
	if _, err = admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	config.RuntimeParams["search_path"] = schema
	db := sqlx.NewDb(stdlib.OpenDB(*config), "pgx")
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close() })
	if err = migrations.Run(t.Context(), db.DB, false); err != nil {
		t.Fatal(err)
	}
	m := &authRecordingMailer{codes: make(map[string]string)}
	s := &Service{DB: db, Mailer: m, Config: Config{
		HMACSecret: bytes.Repeat([]byte{42}, 32), CodeTTL: 10 * time.Minute, MaxAttempts: 5,
		ResendInterval: time.Minute, Lockout: 15 * time.Minute, SessionTTL: time.Hour,
		RequestTimeout: 5 * time.Second, RateWindow: time.Minute, RateLimit: 1000,
	}}
	return s, m
}

const authTestPassword = "correct horse battery staple"
const authTestIP = "192.0.2.1"

func TestAuthParallelLoginAndSessionExpiry(t *testing.T) {
	s, m := authFixture(t)
	email := "parallel-login@example.com"
	authRegister(t, s, email)
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.StartLogin(t.Context(), email, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	in := CodeInput{Email: email, Code: m.Code(email)}
	start := make(chan struct{})
	tokens := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			token, err := s.FinishLogin(t.Context(), in, challenge)
			tokens <- token
			errs <- err
		}()
	}
	close(start)
	var token string
	for range 2 {
		if candidate := <-tokens; candidate != "" {
			if token != "" {
				t.Fatal("OTP created two sessions")
			}
			token = candidate
		}
		if err := <-errs; err != nil {
			authWantStatus(t, err, 401)
		}
	}
	actor, err := s.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.GetContext(t.Context(), &count, `SELECT count(*) FROM sessions`); err != nil || count != 1 {
		t.Fatalf("session count = %d, error = %v", count, err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE session_id=$1`, actor.SessionID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(t.Context(), token)
	authWantStatus(t, err, 401)
}

func authRegister(t *testing.T, s *Service, email string) int64 {
	t.Helper()
	id, err := s.Register(t.Context(), RegisterInput{Name: "Test Client", Email: email, Password: authTestPassword, Phone: "+1 202 555 0100"}, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func authWantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Status != status {
		t.Fatalf("error = %v, want status %d", err, status)
	}
}

func authAllowResend(t *testing.T, s *Service, email, purpose string) {
	t.Helper()
	_, err := s.DB.ExecContext(t.Context(), `UPDATE verification_codes SET regenerate_available_at=clock_timestamp()-interval '1 second' WHERE user_id=(SELECT user_id FROM users WHERE email=$1) AND purpose=$2`, email, purpose)
	if err != nil {
		t.Fatal(err)
	}
}

func authLogin(t *testing.T, s *Service, m *authRecordingMailer, email string) string {
	t.Helper()
	challenge, err := s.StartLogin(t.Context(), email, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.FinishLogin(t.Context(), CodeInput{Email: email, Code: m.Code(email)}, challenge)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAuthRegistrationAndParallelUse(t *testing.T) {
	s, m := authFixture(t)
	email := "client@example.com"
	id := authRegister(t, s, " Client@Example.COM ")
	var role, hash string
	if err := s.DB.QueryRowxContext(t.Context(), `SELECT role,password_hash FROM users JOIN clients USING(user_id) WHERE user_id=$1`, id).Scan(&role, &hash); err != nil {
		t.Fatal(err)
	}
	if role != "client" || !checkPassword(hash, authTestPassword) {
		t.Fatal("registration contract violated")
	}
	if _, err := s.StartLogin(t.Context(), email, authTestPassword, authTestIP); err == nil {
		t.Fatal("unverified login accepted")
	}
	in := CodeInput{Email: email, Code: m.Code(email)}
	var successes atomic.Int32
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := s.VerifyEmail(t.Context(), in)
			if err == nil {
				successes.Add(1)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	if successes.Load() != 1 {
		t.Fatalf("successful OTP uses = %d", successes.Load())
	}
	for err := range errs {
		if err != nil {
			authWantStatus(t, err, 401)
		}
	}
	token := authLogin(t, s, m, email)
	actor, err := s.Authenticate(t.Context(), token)
	if err != nil || actor.UserID != id || actor.Role != "client" {
		t.Fatalf("authenticate: %+v %v", actor, err)
	}
	if err = s.Logout(t.Context(), actor); err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(t.Context(), token)
	authWantStatus(t, err, 401)
}

func TestAuthAttemptsAndCooldown(t *testing.T) {
	s, m := authFixture(t)
	email := "attempts@example.com"
	authRegister(t, s, email)
	wrong := "000000"
	if m.Code(email) == wrong {
		wrong = "000001"
	}
	for i := 1; i <= 5; i++ {
		err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: wrong})
		status := 401
		if i == 5 {
			status = 429
		}
		authWantStatus(t, err, status)
		var attempts int
		if err := s.DB.GetContext(t.Context(), &attempts, `SELECT failed_attempts FROM verification_codes`); err != nil {
			t.Fatal(err)
		}
		if attempts != i {
			t.Fatalf("attempts = %d, want %d", attempts, i)
		}
	}
	var before, after time.Time
	if err := s.DB.GetContext(t.Context(), &before, `SELECT regenerate_available_at FROM verification_codes`); err != nil {
		t.Fatal(err)
	}
	if time.Until(before) < 14*time.Minute {
		t.Fatal("lockout too short")
	}
	authWantStatus(t, s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}), 429)
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 429)
	if err := s.DB.GetContext(t.Context(), &after, `SELECT regenerate_available_at FROM verification_codes`); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("blocked attempts extended lockout")
	}
	authAllowResend(t, s, email, "verify_email")
	if err := s.RequestCode(t.Context(), email, "verify_email", authTestIP); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthResendVersusFifthAttempt(t *testing.T) {
	s, m := authFixture(t)
	verifier := *s
	verifier.Config.HMACSecret = bytes.Repeat([]byte{99}, 32)
	email := "race@example.com"
	authRegister(t, s, email)
	wrong := "000000"
	if m.Code(email) == wrong {
		wrong = "000001"
	}
	for range 4 {
		authWantStatus(t, s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: wrong}), 401)
	}
	authAllowResend(t, s, email, "verify_email")
	start := make(chan struct{})
	errs := make(chan error, 2)
	// A different verifier secret guarantees a mismatch even if the new random code equals wrong.
	go func() { <-start; errs <- verifier.VerifyEmail(t.Context(), CodeInput{Email: email, Code: wrong}) }()
	go func() { <-start; errs <- s.RequestCode(t.Context(), email, "verify_email", authTestIP) }()
	close(start)
	for range 2 {
		err := <-errs
		if err != nil {
			var api *APIError
			if !errors.As(err, &api) || (api.Status != 401 && api.Status != 429) {
				t.Fatal(err)
			}
		}
	}
	var count, attempts int
	if err := s.DB.GetContext(t.Context(), &count, `SELECT count(*) FROM verification_codes`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.GetContext(t.Context(), &attempts, `SELECT failed_attempts FROM verification_codes ORDER BY verification_code_id DESC LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if count == 1 && attempts != 5 {
		t.Fatalf("fifth attempt lost: %d", attempts)
	}
	if count == 2 && attempts != 1 {
		t.Fatalf("resend first must account for failed attempt: %d", attempts)
	}
	if count != 1 && count != 2 {
		t.Fatalf("unexpected issuance count %d", count)
	}
}

func TestAuthPurposeEmailChallengeAndLatestBinding(t *testing.T) {
	s, m := authFixture(t)
	a, b := "a@example.com", "b@example.com"
	authRegister(t, s, a)
	authRegister(t, s, b)
	aCode := m.Code(a)
	// Force distinct codes rather than relying on random codes never colliding.
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE verification_codes SET code_hash=$1 WHERE email=$2`, codeHash(s.Config.HMACSecret, 2, "999999"), b); err != nil {
		t.Fatal(err)
	}
	if aCode == "999999" {
		aCode = "999998"
	}
	authWantStatus(t, s.VerifyEmail(t.Context(), CodeInput{Email: b, Code: aCode}), 401)
	authWantStatus(t, s.ResetPassword(t.Context(), CodeInput{Email: a, Code: m.Code(a)}, "a new secure password"), 401)
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: a, Code: m.Code(a)}); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.StartLogin(t.Context(), a, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	loginCode := m.Code(a)
	_, err = s.FinishLogin(t.Context(), CodeInput{Email: a, Code: loginCode}, "")
	authWantStatus(t, err, 401)
	other, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FinishLogin(t.Context(), CodeInput{Email: a, Code: loginCode}, other)
	authWantStatus(t, err, 401)
	authAllowResend(t, s, a, "login")
	newChallenge, err := s.StartLogin(t.Context(), a, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FinishLogin(t.Context(), CodeInput{Email: a, Code: m.Code(a)}, challenge)
	authWantStatus(t, err, 401)
	if _, err = s.FinishLogin(t.Context(), CodeInput{Email: a, Code: m.Code(a)}, newChallenge); err != nil {
		t.Fatal(err)
	}
}

func TestAuthResetRevokesSessionsAndLoginChallenges(t *testing.T) {
	s, m := authFixture(t)
	email := "reset@example.com"
	authRegister(t, s, email)
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal(err)
	}
	token := authLogin(t, s, m, email)
	authAllowResend(t, s, email, "login")
	challenge, err := s.StartLogin(t.Context(), email, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	loginCode := m.Code(email)
	if err = s.RequestCode(t.Context(), email, "reset_password", authTestIP); err != nil {
		t.Fatal(err)
	}
	in := CodeInput{Email: email, Code: m.Code(email)}
	if err = s.ResetPassword(t.Context(), in, "a different secure password"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(t.Context(), token)
	authWantStatus(t, err, 401)
	_, err = s.FinishLogin(t.Context(), CodeInput{Email: email, Code: loginCode}, challenge)
	authWantStatus(t, err, 401)
	authWantStatus(t, s.ResetPassword(t.Context(), in, "another secure password"), 401)
	_, err = s.StartLogin(t.Context(), email, authTestPassword, authTestIP)
	authWantStatus(t, err, 401)
	authAllowResend(t, s, email, "login")
	if _, err = s.StartLogin(t.Context(), email, "a different secure password", authTestIP); err != nil {
		t.Fatal(err)
	}
}

func TestAuthResetVersusLoginCompletion(t *testing.T) {
	s, m := authFixture(t)
	email := "reset-race@example.com"
	authRegister(t, s, email)
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.StartLogin(t.Context(), email, authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	login := CodeInput{Email: email, Code: m.Code(email)}
	if err = s.RequestCode(t.Context(), email, "reset_password", authTestIP); err != nil {
		t.Fatal(err)
	}
	reset := CodeInput{Email: email, Code: m.Code(email)}
	start := make(chan struct{})
	type loginResult struct {
		token string
		err   error
	}
	loginDone := make(chan loginResult, 1)
	resetDone := make(chan error, 1)
	go func() {
		<-start
		token, err := s.FinishLogin(t.Context(), login, challenge)
		loginDone <- loginResult{token, err}
	}()
	go func() { <-start; resetDone <- s.ResetPassword(t.Context(), reset, "replacement secure password") }()
	close(start)
	result := <-loginDone
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if result.err != nil {
		authWantStatus(t, result.err, 401)
	} else {
		_, err := s.Authenticate(t.Context(), result.token)
		authWantStatus(t, err, 401)
	}
	var active int
	if err := s.DB.GetContext(t.Context(), &active, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("login race escaped reset session revocation")
	}
}

func TestAuthExpiryAndRevokedCodeCooldown(t *testing.T) {
	s, m := authFixture(t)
	email := "expiry@example.com"
	authRegister(t, s, email)
	in := CodeInput{Email: email, Code: m.Code(email)}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE verification_codes SET created_at=clock_timestamp()-interval '20 minutes',expires_at=clock_timestamp()-interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	authWantStatus(t, s.VerifyEmail(t.Context(), in), 401)
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 429)
	authAllowResend(t, s, email, "verify_email")
	if err := s.RequestCode(t.Context(), email, "verify_email", authTestIP); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal(err)
	}
	token := authLogin(t, s, m, email)
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Authenticate(t.Context(), token)
	authWantStatus(t, err, 401)
}

func TestAuthSMTPFailurePreservesCooldownAndUsedRace(t *testing.T) {
	s, m := authFixture(t)
	email := "smtp@example.com"
	m.fail = true
	_, err := s.Register(t.Context(), RegisterInput{Name: "Test", Email: email, Password: authTestPassword, Phone: "1234567890"}, authTestIP)
	authWantStatus(t, err, 503)
	var revoked bool
	if err = s.DB.GetContext(t.Context(), &revoked, `SELECT revoked_at IS NOT NULL FROM verification_codes`); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("SMTP failure left usable code")
	}
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 429)
	authAllowResend(t, s, email, "verify_email")
	m.hook = func() {
		if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
			t.Error(err)
		}
	}
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 503)
	var used, latestRevoked bool
	if err = s.DB.QueryRowxContext(t.Context(), `SELECT used_at IS NOT NULL,revoked_at IS NOT NULL FROM verification_codes ORDER BY verification_code_id DESC LIMIT 1`).Scan(&used, &latestRevoked); err != nil {
		t.Fatal(err)
	}
	if !used || latestRevoked {
		t.Fatal("SMTP cleanup modified a successfully used code")
	}
}

func TestAuthSMTPFailurePreservesFifthAttemptLockout(t *testing.T) {
	s, m := authFixture(t)
	email := "smtp-lockout@example.com"
	authRegister(t, s, email)
	authAllowResend(t, s, email, "verify_email")
	m.fail = true
	var lockedUntil time.Time
	m.hook = func() {
		wrong := "000000"
		if m.Code(email) == wrong {
			wrong = "000001"
		}
		for i := range 5 {
			status := 401
			if i == 4 {
				status = 429
			}
			authWantStatus(t, s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: wrong}), status)
		}
		if err := s.DB.GetContext(t.Context(), &lockedUntil, `SELECT regenerate_available_at FROM verification_codes ORDER BY verification_code_id DESC LIMIT 1`); err != nil {
			t.Fatal(err)
		}
	}
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 503)
	var attempts int
	var until time.Time
	var revoked bool
	if err := s.DB.QueryRowxContext(t.Context(), `SELECT failed_attempts,regenerate_available_at,revoked_at IS NOT NULL FROM verification_codes ORDER BY verification_code_id DESC LIMIT 1`).Scan(&attempts, &until, &revoked); err != nil {
		t.Fatal(err)
	}
	if attempts != 5 || !revoked || !until.Equal(lockedUntil) || time.Until(until) < 14*time.Minute {
		t.Fatal("SMTP cleanup changed fifth-attempt lockout")
	}
	authWantStatus(t, s.RequestCode(t.Context(), email, "verify_email", authTestIP), 429)
}

func TestAuthSMTPFailureDoesNotRevokeReplacement(t *testing.T) {
	s, m := authFixture(t)
	email := "smtp-replaced@example.com"
	m.fail = true
	m.hook = func() {
		m.mu.Lock()
		m.hook, m.fail = nil, false
		m.mu.Unlock()
		authAllowResend(t, s, email, "verify_email")
		if err := s.RequestCode(t.Context(), email, "verify_email", authTestIP); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Register(t.Context(), RegisterInput{Name: "Test", Email: email, Password: authTestPassword, Phone: "1234567890"}, authTestIP)
	authWantStatus(t, err, 503)
	if err := s.VerifyEmail(t.Context(), CodeInput{Email: email, Code: m.Code(email)}); err != nil {
		t.Fatal("SMTP cleanup revoked replacement:", err)
	}
}

func TestAuthNilMailerFailsClosed(t *testing.T) {
	s, _ := authFixture(t)
	s.Mailer = nil
	_, err := s.Register(t.Context(), RegisterInput{Name: "Test", Email: "nil-mailer@example.com", Password: authTestPassword, Phone: "1234567890"}, authTestIP)
	authWantStatus(t, err, 503)
	var revoked bool
	if err := s.DB.GetContext(t.Context(), &revoked, `SELECT revoked_at IS NOT NULL FROM verification_codes`); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("nil mailer left usable OTP")
	}
}

func TestAuthDistributedRateLimitAndUnknownEmail(t *testing.T) {
	s, _ := authFixture(t)
	if err := s.RequestCode(t.Context(), "unknown@example.com", "reset_password", authTestIP); err != nil {
		t.Fatal(err)
	}
	authWantStatus(t, s.RequestCode(t.Context(), "unknown@example.com", "login", authTestIP), 422)
	s.Config.RateLimit = 3
	other := &Service{DB: s.DB, Config: s.Config, Mailer: s.Mailer}
	var successes atomic.Int32
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		instance := s
		if i%2 == 0 {
			instance = other
		}
		wg.Go(func() {
			err := instance.RateLimit(t.Context(), authTestIP, "shared")
			if err == nil {
				successes.Add(1)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	if successes.Load() != 3 {
		t.Fatalf("allowed %d requests", successes.Load())
	}
	for err := range errs {
		if err != nil {
			authWantStatus(t, err, 429)
			var api *APIError
			errors.As(err, &api)
			if api.RetryAfter <= 0 {
				t.Fatal("missing RetryAfter")
			}
		}
	}
}
