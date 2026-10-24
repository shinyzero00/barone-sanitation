//go:build integration

package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

func authFixture(t *testing.T) *Service {
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
	return &Service{DB: db, Config: Config{
		PublicOrigin: httpTestOrigin, HMACSecret: bytes.Repeat([]byte{42}, 32), SessionTTL: time.Hour,
		RequestTimeout: 5 * time.Second, RateWindow: time.Minute, RateLimit: 1000,
	}}
}

const authTestPassword = "correct horse battery staple"
const authTestIP = "192.0.2.1"

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

func TestAuthPasswordLoginAndSessions(t *testing.T) {
	s := authFixture(t)
	id := authRegister(t, s, " Test@Example.COM ")
	_, err := s.Register(t.Context(), RegisterInput{Name: "Other", Email: "TEST@example.com", Password: authTestPassword, Phone: "123"}, authTestIP)
	authWantStatus(t, err, 409)
	for _, email := range []string{"test@example.com", "unknown@example.com"} {
		_, err := s.Login(t.Context(), email, "incorrect password", authTestIP)
		authWantStatus(t, err, 401)
	}
	var count int
	if err := s.DB.GetContext(t.Context(), &count, `SELECT count(*) FROM sessions`); err != nil || count != 0 {
		t.Fatalf("invalid login created sessions: count=%d err=%v", count, err)
	}
	for _, action := range []string{"logout", "expire"} {
		t.Run(action, func(t *testing.T) {
			token, err := s.Login(t.Context(), "TEST@example.com", authTestPassword, authTestIP)
			if err != nil {
				t.Fatal(err)
			}
			actor, err := s.Authenticate(t.Context(), token)
			if err != nil || actor.UserID != id || actor.Role != "client" {
				t.Fatalf("actor=%+v err=%v", actor, err)
			}
			profile, err := s.GetProfile(t.Context(), actor)
			if err != nil || profile.Email != "test@example.com" || profile.Phone != "+1 202 555 0100" || profile.Name != "Test Client" {
				t.Fatalf("profile=%+v err=%v", profile, err)
			}
			var digest []byte
			if err := s.DB.GetContext(t.Context(), &digest, `SELECT token_hash FROM sessions WHERE session_id=$1`, actor.SessionID); err != nil || !bytes.Equal(digest, tokenHash(token)) {
				t.Fatalf("session digest=%x err=%v", digest, err)
			}
			if action == "logout" {
				if err := s.Logout(t.Context(), actor); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.DB.ExecContext(t.Context(), `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE session_id=$1`, actor.SessionID); err != nil {
				t.Fatal(err)
			}
			_, err = s.Authenticate(t.Context(), token)
			authWantStatus(t, err, 401)
		})
	}
}

func TestAuthLoginRevalidatesPasswordSnapshot(t *testing.T) {
	s := authFixture(t)
	id := authRegister(t, s, "snapshot@example.com")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err := tx.GetContext(ctx, &blocker, `SELECT pg_backend_pid() FROM users WHERE user_id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Login(ctx, "snapshot@example.com", authTestPassword, authTestIP)
		done <- err
	}()
	// Observe the lock wait rather than assuming Argon2 finishes within a sleep.
	for {
		var waiting bool
		if err := s.DB.GetContext(ctx, &waiting, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT user_id,email,password_hash FROM users%')`, blocker); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("login did not wait for the user lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash='changed snapshot' WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	authWantStatus(t, <-done, 401)
	var count int
	if err := s.DB.GetContext(ctx, &count, `SELECT count(*) FROM sessions`); err != nil || count != 0 {
		t.Fatalf("stale password created sessions: count=%d err=%v", count, err)
	}
}

func TestAuthSharedRateLimit(t *testing.T) {
	s := authFixture(t)
	s.Config.RateLimit = 1
	other := &Service{DB: s.DB, Config: s.Config}
	if err := s.RateLimit(t.Context(), authTestIP, "login"); err != nil {
		t.Fatal(err)
	}
	authWantStatus(t, other.RateLimit(t.Context(), authTestIP, "login"), 429)
	if err := other.RateLimit(t.Context(), "192.0.2.2", "login"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE ip_rate_limits SET window_started_at=clock_timestamp()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if err := other.RateLimit(t.Context(), authTestIP, "login"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPPasswordSession(t *testing.T) {
	s := authFixture(t)
	app := NewApp(s)
	csrf := httpCSRFTestCookie(t, app)
	httpDo(t, app, httpUnsafeRequest("POST", "/api/v1/auth/register", `{"name":"Test Client","email":"Test@example.com","password":"correct horse battery staple","phone":"123","role":"admin"}`, csrf), 400)
	httpDo(t, app, httpUnsafeRequest("POST", "/api/v1/auth/register", `{"name":"Test Client","email":"Test@example.com","password":"correct horse battery staple","phone":"123"}`, csrf), 201)
	httpDo(t, app, httpUnsafeRequest("POST", "/api/v1/auth/login", `{"email":"test@example.com","password":"wrong password"}`, csrf), 401)
	response := httpDo(t, app, httpUnsafeRequest("POST", "/api/v1/auth/login", `{"email":"test@example.com","password":"correct horse battery staple"}`, csrf), 204)
	body, err := io.ReadAll(response.Body)
	if err != nil || len(body) != 0 {
		t.Fatalf("login body=%s err=%v", body, err)
	}
	var session *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookie {
			session = cookie
		}
	}
	if session == nil || !validToken(session.Value) || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" || session.Domain != "" || session.MaxAge != 3600 {
		t.Fatalf("unsafe session cookie: %+v", session)
	}
	httpDo(t, app, httptest.NewRequest("GET", "/api/v1/me", nil), 401)
	request := httptest.NewRequest("GET", "/api/v1/me", nil)
	request.AddCookie(session)
	response = httpDo(t, app, request, 200)
	var profile Profile
	if err := json.NewDecoder(response.Body).Decode(&profile); err != nil || profile.Role != "client" || profile.Email != "test@example.com" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	request = httpUnsafeRequest("POST", "/api/v1/auth/logout", "", csrf)
	request.AddCookie(session)
	response = httpDo(t, app, request, 204)
	if cookies := response.Cookies(); len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].MaxAge >= 0 || cookies[0].Expires.After(time.Now()) {
		t.Fatalf("session not cleared: %+v", cookies)
	}
	request = httptest.NewRequest("GET", "/api/v1/me", nil)
	request.AddCookie(session)
	httpDo(t, app, request, 401)
	// Expiry is checked again on the next protected request, not cached in the app.
	token, err := s.Login(t.Context(), "test@example.com", authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE token_hash=$1`, tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/api/v1/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	httpDo(t, app, request, 401)
}
