//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func serverTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_TEST_URL")
	if dsn == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("DATABASE_TEST_URL must be a PostgreSQL URL")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse test database configuration")
	}
	root := stdlib.OpenDB(*config)
	t.Cleanup(func() { root.Close() })
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("cannot generate schema name")
	}
	schema := "server_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := root.ExecContext(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := root.ExecContext(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("cannot drop isolated test schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", u.String())
	config, err = pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal("cannot parse isolated database configuration")
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestServerMigrationsAndAdmin(t *testing.T) {
	db := serverTestDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	version := func(want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&got); err != nil {
			t.Fatal("cannot read migration version")
		}
		if got != want {
			t.Fatalf("migration version = %d, want %d", got, want)
		}
	}
	for range 2 {
		if err := run([]string{"migrate", "up"}); err != nil {
			t.Fatal("migrate up failed")
		}
		version(6)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (name,email,password_hash,email_verified_at) VALUES
		('Verified','verified@server.test','unused',clock_timestamp()),
		('Unverified','unverified@server.test','unused',NULL)`); err != nil {
		t.Fatal("cannot insert test users")
	}
	if err := run([]string{"admin", " Verified@Server.Test "}); err != nil {
		t.Fatal("verified account promotion failed")
	}
	for _, email := range []string{"unverified@server.test", "missing@server.test"} {
		if err := run([]string{"admin", email}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("admin must reject an unverified or missing account with no rows")
		}
	}
	var verified, unverified string
	if err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE email='verified@server.test'`).Scan(&verified); err != nil {
		t.Fatal("cannot read verified account role")
	}
	if err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE email='unverified@server.test'`).Scan(&unverified); err != nil {
		t.Fatal("cannot read unverified account role")
	}
	if verified != "admin" || unverified != "client" {
		t.Fatal("admin command changed the wrong account roles")
	}
	for _, want := range []int64{5, 4, 3, 2, 1, 0} {
		if err := run([]string{"migrate", "down"}); err != nil {
			t.Fatal("migrate down failed")
		}
		version(want)
	}
	if err := run([]string{"migrate", "up"}); err != nil {
		t.Fatal("migrate up after rollback failed")
	}
	version(6)
}

// Keep Fiber's startup banner bounded and private, including on failure.
type serverTestOutput struct{ buffer bytes.Buffer }

func (b *serverTestOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(len(p), remaining)])
	}
	return n, nil
}

func TestServerSmoke(t *testing.T) {
	serverTestDatabase(t)
	if err := run([]string{"migrate", "up"}); err != nil {
		t.Fatal("migrate up failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve test listen address")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("cannot release test listen address")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal("cannot generate test HMAC secret")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestServerProcess$")
	cmd.Env = append(os.Environ(),
		"BARONE_SERVER_TEST_PROCESS=serve",
		"LISTEN_ADDRESS="+address,
		"PUBLIC_ORIGIN=https://server.test",
		"OTP_HMAC_SECRET="+base64.StdEncoding.EncodeToString(secret[:]),
		"OTP_PROVIDER=console", "SMTP_ADDRESS=", "SMTP_FROM=",
		"SMTP_USERNAME=", "SMTP_PASSWORD=", "TRUSTED_PROXIES=",
	)
	output := &serverTestOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start server child process")
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/healthz", nil)
		if err != nil {
			t.Fatal("cannot construct health request")
		}
		response, err := client.Do(request)
		if err == nil {
			var health struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Status == "ok" {
				break
			}
		}
		select {
		case <-done:
			t.Fatal("server exited before becoming healthy")
		case <-ctx.Done():
			t.Fatal("server did not become healthy before deadline")
		case <-ticker.C:
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/csrf", nil)
	if err != nil {
		t.Fatal("cannot construct CSRF request")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("CSRF request failed")
	}
	defer response.Body.Close()
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&csrf); err != nil || response.StatusCode != http.StatusOK || csrf.Token == "" {
		t.Fatal("invalid CSRF response")
	}
	for key, want := range map[string]string{
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
		"X-Frame-Options": "DENY", "Strict-Transport-Security": "max-age=31536000",
	} {
		if response.Header.Get(key) != want {
			t.Errorf("incorrect %s header", key)
		}
	}
	var cookie *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "__Host-csrf" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != csrf.Token || !cookie.Secure || cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 86400 {
		t.Fatal("invalid CSRF cookie contract")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("cannot send SIGTERM to server child")
	}
	select {
	case <-done:
		if waitErr != nil {
			t.Fatal("server did not exit successfully after SIGTERM")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not shut down gracefully before deadline")
	}
}

func TestServerProcess(t *testing.T) {
	if os.Getenv("BARONE_SERVER_TEST_PROCESS") != "serve" {
		t.Skip("server subprocess helper")
	}
	if err := run([]string{"serve"}); err != nil {
		t.Fatal("serve failed")
	}
}
