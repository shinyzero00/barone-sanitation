package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

const httpTestOrigin = "https://booking.example.com"

func httpTestService() *Service {
	return &Service{Config: Config{PublicOrigin: httpTestOrigin, HMACSecret: bytes.Repeat([]byte{7}, 32), RequestTimeout: time.Second}}
}

func httpDo(t *testing.T, app *fiber.App, request *http.Request, status int) *http.Response {
	t.Helper()
	response, err := app.Test(request, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s: status %d, want %d; body %s", request.Method, request.URL.Path, response.StatusCode, status, body)
	}
	return response
}

func httpCSRFTestCookie(t *testing.T, app *fiber.App) *http.Cookie {
	t.Helper()
	response := httpDo(t, app, httptest.NewRequest("GET", "/csrf", nil), 200)
	var body struct {
		Token string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == csrfCookie {
			if body.Token != cookie.Value {
				t.Fatal("CSRF body and cookie differ")
			}
			return cookie
		}
	}
	t.Fatal("missing CSRF cookie")
	return nil
}

func httpUnsafeRequest(method, path, body string, csrf *http.Cookie) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Origin", httpTestOrigin)
	request.Header.Set("Content-Type", "application/json")
	if csrf != nil {
		request.AddCookie(csrf)
		request.Header.Set("X-CSRF-Token", csrf.Value)
	}
	return request
}

func TestHTTPCSRFAndOrigin(t *testing.T) {
	s := httpTestService()
	app := NewApp(s)
	// A non-API endpoint isolates the security boundary from PostgreSQL rate limiting.
	// Test security acceptance using a non-API 404, and rejection using 403.
	csrf := httpCSRFTestCookie(t, app)
	if !csrf.Secure || csrf.HttpOnly || csrf.SameSite != http.SameSiteStrictMode || csrf.Path != "/" || csrf.Domain != "" {
		t.Fatalf("unsafe CSRF cookie attributes: %+v", csrf)
	}
	for _, tc := range []struct {
		name, origin, header, cookie string
		status                       int
	}{
		{"valid", httpTestOrigin, csrf.Value, csrf.Value, 404},
		{"missing origin", "", csrf.Value, csrf.Value, 403},
		{"subdomain origin", "https://evil.booking.example.com", csrf.Value, csrf.Value, 403},
		{"origin suffix", httpTestOrigin + ".evil.test", csrf.Value, csrf.Value, 403},
		{"missing cookie", httpTestOrigin, csrf.Value, "", 403},
		{"missing header", httpTestOrigin, "", csrf.Value, 403},
		{"mismatch", httpTestOrigin, csrf.Value + "x", csrf.Value, 403},
		{"unsigned match", httpTestOrigin, "attacker", "attacker", 403},
		{"tampered signature", httpTestOrigin, csrf.Value + "x", csrf.Value + "x", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httpUnsafeRequest("POST", "/probe", "{}", nil)
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("X-CSRF-Token", tc.header)
			if tc.cookie != "" {
				request.AddCookie(&http.Cookie{Name: csrfCookie, Value: tc.cookie})
			}
			response := httpDo(t, app, request, tc.status)
			if response.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("CORS unexpectedly enabled")
			}
			for header, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Strict-Transport-Security": "max-age=31536000"} {
				if response.Header.Get(header) != want {
					t.Errorf("%s = %q", header, response.Header.Get(header))
				}
			}
		})
	}
	// Tokens cannot be reused against a different deployment secret.
	if httpValidCSRF(bytes.Repeat([]byte{8}, 32), csrf.Value) {
		t.Fatal("cross-secret CSRF accepted")
	}
	for _, path := range []string{"auth/register", "auth/login", "auth/logout"} {
		t.Run(path, func(t *testing.T) {
			request := httpUnsafeRequest("POST", "/api/v1/"+path, "{}", nil)
			httpDo(t, app, request, 403)
		})
	}
}

func TestHTTPStrictJSON(t *testing.T) {
	app := fiber.New(fiber.Config{BodyLimit: httpBodyLimit, ErrorHandler: httpError})
	app.Post("/", func(c fiber.Ctx) error {
		var in RegisterInput
		if err := httpDecode(c, &in); err != nil {
			return err
		}
		return c.JSON(in)
	})
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"valid", `{"email":"a@example.com","password":"a password here"}`, "application/json", 200},
		{"charset", `{}`, "application/json; charset=utf-8", 200},
		{"unknown", `{"user_id":1}`, "application/json", 400},
		{"role override", `{"role":"admin"}`, "application/json", 400},
		{"malformed", `{"email":`, "application/json", 400},
		{"wrong type", `{"password":123456}`, "application/json", 400},
		{"multiple objects", `{} {}`, "application/json", 400},
		{"trailing null", `{} null`, "application/json", 400},
		{"trailing garbage", `{} x`, "application/json", 400},
		{"null", `null`, "application/json", 400},
		{"array", `[]`, "application/json", 400},
		{"empty", ``, "application/json", 400},
		{"form", `email=a`, "application/x-www-form-urlencoded", 415},
		{"missing type", `{}`, "", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			httpDo(t, app, request, tc.status)
		})
	}
}

func TestHTTPBodyLimit(t *testing.T) {
	app := NewApp(httpTestService())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	request, err := http.NewRequest("POST", "http://"+listener.Addr().String()+"/api/v1/auth/register", strings.NewReader(strings.Repeat("a", httpBodyLimit+1)))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 413 || string(body) != `{"error":"request_too_large"}` {
		t.Fatalf("oversized request: status %d body %s", response.StatusCode, body)
	}
	if response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("server-level error lost security headers")
	}
}

func TestHTTPProxyChain(t *testing.T) {
	for _, tc := range []struct {
		name, peer, xff string
		proxies         []string
		want            string
	}{
		{"untrusted peer", "192.0.2.1", "198.51.100.1", []string{"10.0.0.0/8"}, "192.0.2.1"},
		{"no config", "10.0.0.1", "198.51.100.1", nil, "10.0.0.1"},
		{"trusted peer", "10.0.0.1", "198.51.100.1", []string{"10.0.0.1"}, "198.51.100.1"},
		{"forged leftmost", "10.0.0.1", "203.0.113.99, 198.51.100.1, 10.0.0.2", []string{"10.0.0.0/8"}, "198.51.100.1"},
		{"malformed adjacent", "10.0.0.1", "198.51.100.1, unknown", []string{"10.0.0.0/8"}, "10.0.0.1"},
		{"empty hop", "10.0.0.1", "198.51.100.1,", []string{"10.0.0.0/8"}, "10.0.0.1"},
		{"all trusted", "10.0.0.1", "10.0.0.2", []string{"10.0.0.0/8"}, "10.0.0.1"},
		{"ipv6", "::1", "2001:db8::1, ::1", []string{"::1/128"}, "2001:db8::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpClientIP(net.ParseIP(tc.peer), tc.xff, tc.proxies); got != tc.want {
				t.Fatalf("IP = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestHTTPGenericErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		status      int
		body, retry string
	}{
		{"internal", errors.New("password=secret code=123456 SQL token=secret"), 500, `{"error":"internal_error"}`, ""},
		{"wrapped api", fmt.Errorf("private: %w", &APIError{Status: 429, Code: "rate_limited", RetryAfter: 1100 * time.Millisecond}), 429, `{"error":"rate_limited"}`, "2"},
		{"framework", fiber.NewError(400, "private field data"), 400, `{"error":"bad_request"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: httpError})
			app.Get("/", func(fiber.Ctx) error { return tc.err })
			response := httpDo(t, app, httptest.NewRequest("GET", "/", nil), tc.status)
			body, _ := io.ReadAll(response.Body)
			if string(body) != tc.body || response.Header.Get("Retry-After") != tc.retry {
				t.Fatalf("body=%s retry=%s", body, response.Header.Get("Retry-After"))
			}
		})
	}
	app := NewApp(httpTestService())
	// A nil DB panics on health checks, and must never expose a stack or panic value.
	response := httpDo(t, app, httptest.NewRequest("GET", "/healthz", nil), 500)
	body, _ := io.ReadAll(response.Body)
	if string(body) != `{"error":"internal_error"}` {
		t.Fatalf("panic leaked: %s", body)
	}
}

func TestHTTPCookies(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		httpSetCookie(c, sessionCookie, "opaque", time.Hour, true)
		return c.SendStatus(204)
	})
	response := httpDo(t, app, httptest.NewRequest("GET", "/", nil), 204)
	if len(response.Cookies()) != 1 {
		t.Fatal("missing cookies")
	}
	for _, cookie := range response.Cookies() {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("unsafe cookie attributes: %+v", cookie)
		}
		if cookie.Name == sessionCookie && cookie.MaxAge != 3600 {
			t.Fatal("incorrect session TTL")
		}
	}
}
