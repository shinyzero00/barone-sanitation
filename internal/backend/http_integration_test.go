//go:build integration

package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

type httpTestBrowser struct {
	app     *fiber.App
	csrf    *http.Cookie
	cookies map[string]*http.Cookie
}

func newHTTPBrowser(t *testing.T, app *fiber.App) *httpTestBrowser {
	t.Helper()
	return &httpTestBrowser{app: app, csrf: httpCSRFTestCookie(t, app), cookies: make(map[string]*http.Cookie)}
}

func (b *httpTestBrowser) request(t *testing.T, method, path string, in any, status int, out any) *http.Response {
	t.Helper()
	body := ""
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	request := httpUnsafeRequest(method, path, body, b.csrf)
	for _, cookie := range b.cookies {
		request.AddCookie(cookie)
	}
	response := httpDo(t, b.app, request, status)
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	} else if status == 204 {
		content, err := io.ReadAll(response.Body)
		if err != nil || len(content) != 0 {
			t.Fatal("authentication response must have no body")
		}
	}
	return response
}

func (b *httpTestBrowser) registerLogin(t *testing.T, mailer *authRecordingMailer, email string) int64 {
	t.Helper()
	var registered map[string]int64
	b.request(t, "POST", "/api/v1/auth/register", RegisterInput{Name: "HTTP Client", Email: email, Password: authTestPassword, Phone: "+12025550100"}, 201, &registered)
	if len(registered) != 1 || registered["user_id"] <= 0 {
		t.Fatal("registration exposed unexpected fields")
	}
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": authTestPassword}, 401, nil)
	b.request(t, "POST", "/api/v1/auth/verify-email", CodeInput{Email: email, Code: mailer.Code(email)}, 204, nil)
	b.login(t, mailer, email, authTestPassword)
	return registered["user_id"]
}

func (b *httpTestBrowser) login(t *testing.T, mailer *authRecordingMailer, email, password string) {
	t.Helper()
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, 204, nil)
	challenge := b.cookies[challengeCookie]
	if challenge == nil || !challenge.Secure || !challenge.HttpOnly || challenge.SameSite != http.SameSiteStrictMode || challenge.Path != "/" || challenge.Domain != "" || challenge.MaxAge != 600 {
		t.Fatal("login challenge cookie contract violated")
	}
	// A valid OTP without the browser challenge is not sufficient.
	delete(b.cookies, challengeCookie)
	b.request(t, "POST", "/api/v1/auth/login/verify", CodeInput{Email: email, Code: mailer.Code(email)}, 401, nil)
	b.cookies[challengeCookie] = challenge
	b.request(t, "POST", "/api/v1/auth/login/verify", CodeInput{Email: email, Code: mailer.Code(email)}, 204, nil)
	if b.cookies[challengeCookie] != nil {
		t.Fatal("challenge was not cleared")
	}
	session := b.cookies[sessionCookie]
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" || session.Domain != "" || session.MaxAge != 3600 {
		t.Fatal("session cookie contract violated")
	}
}

func TestHTTPAuthEndToEnd(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	app := NewApp(s)
	b := newHTTPBrowser(t, app)
	email := "http@example.com"
	id := b.registerLogin(t, mailer, email)
	var profile Profile
	b.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	if profile.UserID != id || profile.Role != "client" {
		t.Fatal("wrong authenticated profile")
	}
	session := b.cookies[sessionCookie]
	b.request(t, "POST", "/api/v1/auth/logout", nil, 204, nil)
	b.cookies[sessionCookie] = session
	b.request(t, "GET", "/api/v1/me", nil, 401, nil)
	delete(b.cookies, sessionCookie)
	authAllowResend(t, s, email, "login")
	b.login(t, mailer, email, authTestPassword)
	b.request(t, "POST", "/api/v1/auth/codes", map[string]string{"email": email, "purpose": "reset_password"}, 204, nil)
	b.request(t, "POST", "/api/v1/auth/reset-password", map[string]string{"email": email, "code": mailer.Code(email), "password": "replacement secure password"}, 204, nil)
	b.request(t, "GET", "/api/v1/me", nil, 401, nil)
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": authTestPassword}, 401, nil)
	authAllowResend(t, s, email, "login")
	b.login(t, mailer, email, "replacement secure password")
	b.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	httpDo(t, app, httptest.NewRequest("GET", "/healthz", nil), 200)
	// Public APIs share the global budget, and verification has a separate common budget.
	var buckets []string
	if err := s.DB.SelectContext(t.Context(), &buckets, `SELECT DISTINCT bucket FROM ip_rate_limits ORDER BY bucket`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(buckets, ","), "http") || !strings.Contains(strings.Join(buckets, ","), "verify") {
		t.Fatal("missing HTTP rate budgets")
	}
}

func TestHTTPRateBudgetsAndSQLTimeout(t *testing.T) {
	s, _ := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	s.Config.RateLimit = 1
	app := NewApp(s)
	httpDo(t, app, httptest.NewRequest("GET", "/api/v1/auth/csrf", nil), 200)
	response := httpDo(t, app, httptest.NewRequest("GET", "/api/v1/auth/csrf", nil), 429)
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("missing retry header")
	}
	s.Config.RateLimit = 10
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO ip_rate_limits(requested_ip,bucket,window_started_at,request_count)
		SELECT requested_ip,'verify',clock_timestamp(),10 FROM ip_rate_limits WHERE bucket='http'`); err != nil {
		t.Fatal(err)
	}
	browser := newHTTPBrowser(t, app)
	for _, path := range []string{"verify-email", "login/verify", "reset-password"} {
		browser.request(t, "POST", "/api/v1/auth/"+path, CodeInput{Email: "unknown@http.test", Code: "123456"}, 429, nil)
	}
	// Exhausting the pool verifies the HTTP standard-context deadline reaches SQL.
	s.DB.SetMaxOpenConns(1)
	conn, err := s.DB.Connx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.Config.RequestTimeout = 25 * time.Millisecond
	app = NewApp(s)
	started := time.Now()
	httpDo(t, app, httptest.NewRequest("GET", "/healthz", nil), 503)
	if time.Since(started) > time.Second {
		t.Fatal("SQL ignored HTTP deadline")
	}
}
