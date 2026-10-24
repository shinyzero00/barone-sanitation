//go:build integration

package backend

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

func TestAuthPasswordStageUpgradeRequiresOTP(t *testing.T) {
	s, mailer := authFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		var version int64
		if err := s.DB.GetContext(ctx, &version, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`); err != nil {
			t.Fatal(err)
		}
		if version == 2 {
			break
		}
		if err := migrations.Run(ctx, s.DB.DB, true); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := hashPassword(authTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.DB.QueryRowxContext(ctx, `INSERT INTO users(name,email,password_hash) VALUES ('Existing Client','upgrade@example.com',$1) RETURNING user_id`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO clients(user_id,phone) VALUES ($1,'123')`, id); err != nil {
		t.Fatal(err)
	}
	oldToken, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES ($1,$2,clock_timestamp()+interval '1 hour')`, id, tokenHash(oldToken)); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, s.DB.DB, false); err != nil {
		t.Fatal(err)
	}
	var verified, revoked sql.NullTime
	if err := s.DB.QueryRowxContext(ctx, `SELECT u.email_verified_at,s.revoked_at FROM users u JOIN sessions s USING(user_id) WHERE u.user_id=$1`, id).Scan(&verified, &revoked); err != nil {
		t.Fatal(err)
	}
	if verified.Valid || !revoked.Valid {
		t.Fatalf("upgrade verified existing email or retained session: verified=%v revoked=%v", verified.Valid, revoked.Valid)
	}
	_, err = s.Authenticate(ctx, oldToken)
	authWantStatus(t, err, 401)
	_, err = s.StartLogin(ctx, "upgrade@example.com", authTestPassword, authTestIP)
	authWantStatus(t, err, 401)
	if err := s.RequestCode(ctx, "upgrade@example.com", "verify_email", authTestIP); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, CodeInput{Email: "upgrade@example.com", Code: mailer.Code("upgrade@example.com")}); err != nil {
		t.Fatal(err)
	}
	// Verification alone must not revive a session issued by password-only login.
	_, err = s.Authenticate(ctx, oldToken)
	authWantStatus(t, err, 401)
	challenge, err := s.StartLogin(ctx, "upgrade@example.com", authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	var active int
	if err := s.DB.GetContext(ctx, &active, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`); err != nil || active != 0 {
		t.Fatalf("password step created active sessions: count=%d err=%v", active, err)
	}
	token, err := s.FinishLogin(ctx, CodeInput{Email: "upgrade@example.com", Code: mailer.Code("upgrade@example.com")}, challenge)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.Authenticate(ctx, token)
	if err != nil || actor.UserID != id {
		t.Fatalf("OTP session actor=%+v err=%v", actor, err)
	}
}

func TestAuthProfileRevalidatesSession(t *testing.T) {
	s, mailer := authFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	id := authRegister(t, s, "profile@example.com")
	if err := s.VerifyEmail(ctx, CodeInput{Email: "profile@example.com", Code: mailer.Code("profile@example.com")}); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.StartLogin(ctx, "profile@example.com", authTestPassword, authTestIP)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.FinishLogin(ctx, CodeInput{Email: "profile@example.com", Code: mailer.Code("profile@example.com")}, challenge)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetProfile(ctx, actor)
	if err != nil || profile.UserID != id || profile.Role != "client" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	for _, tc := range []struct {
		name, query string
	}{
		{"revoked", `UPDATE sessions SET revoked_at=clock_timestamp()`},
		{"expired", `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour'`},
		{"unverified", `UPDATE users SET email_verified_at=NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.DB.ExecContext(ctx, tc.query); err != nil {
				t.Fatal(err)
			}
			_, err := s.GetProfile(ctx, actor)
			authWantStatus(t, err, 401)
			if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET revoked_at=NULL,created_at=clock_timestamp(),expires_at=clock_timestamp()+interval '1 hour'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, `UPDATE users SET email_verified_at=clock_timestamp()`); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, forged := range []Actor{{UserID: id}, {UserID: id + 1, SessionID: actor.SessionID}, {UserID: id, SessionID: actor.SessionID + 1}} {
		_, err := s.GetProfile(ctx, forged)
		authWantStatus(t, err, 401)
	}
}
