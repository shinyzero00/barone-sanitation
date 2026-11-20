package backend

import (
	"bytes"
	"context"
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

	"github.com/danielgtaylor/huma/v2"
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
	for _, path := range []string{"auth/register", "auth/codes", "auth/verify-email", "auth/login", "auth/login/verify", "auth/reset-password", "auth/logout", "bookings", "admin/consultants"} {
		t.Run(path, func(t *testing.T) {
			request := httpUnsafeRequest("POST", "/api/v1/"+path, "{}", nil)
			httpDo(t, app, request, 403)
		})
	}
}

func TestHTTPStrictJSON(t *testing.T) {
	app := fiber.New(fiber.Config{BodyLimit: httpBodyLimit, ErrorHandler: httpError})
	called := false
	httpRegister(newHTTPAPI(app, httpTestService()), huma.Operation{Method: "POST", Path: "/", OperationID: "testCode"}, false,
		func(_ context.Context, in *HTTPBody[CodeInput]) (*httpOutput[CodeInput], error) {
			called = true
			return httpResult(in.Body, nil)
		})
	guarded := NewApp(httpTestService())
	csrf := httpCSRFTestCookie(t, guarded)
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"valid", `{"email":"a@example.com","code":"123456"}`, "application/json", 200},
		{"charset", `{"email":"a@example.com","code":"123456"}`, "application/json; charset=utf-8", 200},
		{"missing fields", `{}`, "application/json", 422},
		{"unknown", `{"email":"a@example.com","code":"123456","password":"private-password","secret":"private-secret"}`, "application/json", 422},
		{"purpose override", `{"email":"a@example.com","code":"123456","purpose":"login"}`, "application/json", 422},
		{"malformed", `{"email":"a@example.com","code":"123456","password":"private-password","secret":"private-secret",`, "application/json", 400},
		{"wrong type", `{"email":"a@example.com","code":123456}`, "application/json", 422},
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
			called = false
			request := httpUnsafeRequest("POST", "/", tc.body, csrf)
			request.Header.Set("Content-Type", tc.contentType)
			target := app
			if tc.name == "null" || tc.name == "array" {
				target = guarded
				request.URL.Path = "/api/v1/auth/verify-email"
			}
			response := httpDo(t, target, request, tc.status)
			if tc.status == 200 {
				if !called {
					t.Fatal("typed handler was not called")
				}
				return
			}
			if called {
				t.Fatal("invalid request reached handler")
			}
			code := map[int]string{400: "invalid_json", 415: "application_json_required", 422: "invalid_request"}[tc.status]
			httpAssertSafeError(t, response, code, "a@example.com", "123456", "private-password", "private-secret")
		})
	}
}

func httpAssertSafeError(t *testing.T, response *http.Response, code string, secrets ...string) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(body), secret) {
			t.Error("error response echoed submitted credentials")
		}
	}
	if strings.TrimSpace(string(body)) != `{"error":"`+code+`"}` {
		t.Fatalf("unsafe error envelope: %s", body)
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
	for _, tc := range []struct {
		method, path string
		limit        int
	}{
		{"POST", "/api/v1/auth/register", httpBodyLimit},
		{"PUT", avatarUploadPath, AvatarMaxBytes},
	} {
		t.Run(tc.path, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, "http://"+listener.Addr().String()+tc.path, strings.NewReader(strings.Repeat("a", tc.limit+1)))
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
		})
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
			if tc.name != "framework" {
				app := fiber.New()
				httpRegister(newHTTPAPI(app, httpTestService()), huma.Operation{Method: "GET", Path: "/", OperationID: "testError"}, false,
					func(context.Context, *struct{}) (*httpOutput[string], error) { return nil, tc.err })
				response := httpDo(t, app, httptest.NewRequest("GET", "/", nil), tc.status)
				body, _ := io.ReadAll(response.Body)
				if strings.TrimSpace(string(body)) != tc.body || response.Header.Get("Retry-After") != tc.retry {
					t.Fatalf("Huma body=%s retry=%s", body, response.Header.Get("Retry-After"))
				}
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

func TestHTTPHumaErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code, retry string
		err               error
		status            int
	}{
		{"internal", "internal_error", "", errors.New("password=private-password code=654321 SQL=private-query"), 500},
		{"retry", "rate_limited", "2", fmt.Errorf("private context: %w", &APIError{Status: 429, Code: "rate_limited", RetryAfter: 1100 * time.Millisecond}), 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			api := newHTTPAPI(app, httpTestService())
			httpRegister(api, huma.Operation{Method: "GET", Path: "/error", OperationID: "testError"}, false,
				func(context.Context, *struct{}) (*struct{}, error) { return nil, tc.err })
			response := httpDo(t, app, httptest.NewRequest("GET", "/error", nil), tc.status)
			httpAssertSafeError(t, response, tc.code, "private-password", "654321", "private-query", "private context")
			if response.Header.Get("Retry-After") != tc.retry {
				t.Fatal("Huma lost retry metadata")
			}
		})
	}
}

func TestHTTPCookies(t *testing.T) {
	app := fiber.New()
	httpRegister(newHTTPAPI(app, httpTestService()), huma.Operation{Method: "GET", Path: "/", OperationID: "testCookies", DefaultStatus: 204}, false,
		func(context.Context, *struct{}) (*HTTPCookieOutput, error) {
			return &HTTPCookieOutput{Cookies: []http.Cookie{
				httpCookie(sessionCookie, "opaque", time.Hour, true),
				httpCookie(challengeCookie, "", -time.Second, true),
			}}, nil
		})
	response := httpDo(t, app, httptest.NewRequest("GET", "/", nil), 204)
	if len(response.Header.Values("Set-Cookie")) != 2 || len(response.Cookies()) != 2 {
		t.Fatal("missing cookies")
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name != sessionCookie && cookie.Name != challengeCookie {
			t.Fatalf("unexpected cookie %q", cookie.Name)
		}
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("unsafe cookie attributes: %+v", cookie)
		}
		if cookie.Name == sessionCookie && (cookie.MaxAge != 3600 || cookie.Value != "opaque") {
			t.Fatal("incorrect session TTL")
		}
		if cookie.Name == challengeCookie && (cookie.Value != "" || cookie.MaxAge >= 0 || cookie.Expires.After(time.Now())) {
			t.Fatal("challenge not cleared")
		}
	}
}

func TestHTTPPositiveIDs(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "+1", "1.0", " 1", "1x", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := httpPositiveID(raw); err == nil {
				t.Fatal("invalid ID accepted")
			}
		})
	}
	if id, err := httpPositiveID("9223372036854775807"); err != nil || id != 9223372036854775807 {
		t.Fatal("valid int64 rejected")
	}
}

func TestHTTPJSONInt64IDs(t *testing.T) {
	app := fiber.New()
	var got BookingInput
	httpRegister(newHTTPAPI(app, httpTestService()), huma.Operation{Method: "POST", Path: "/", OperationID: "testBookingIDs", DefaultStatus: 204}, false,
		func(_ context.Context, in *HTTPBody[BookingInput]) (*struct{}, error) {
			got = in.Body
			return nil, nil
		})
	for _, tc := range []struct {
		name, offering, address string
		status                  int
	}{
		{"max", "9223372036854775807", "9223372036854775807", 204},
		{"offering overflow", "9223372036854775808", "1", 422},
		{"address overflow", "1", "9223372036854775808", 422},
		{"offering underflow", "-9223372036854775809", "1", 422},
		{"address underflow", "1", "-9223372036854775809", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = BookingInput{}
			body := fmt.Sprintf(`{"consultation_offering_id":%s,"client_address_id":%s,"starts_at":"2026-11-20T10:00:00Z"}`, tc.offering, tc.address)
			response := httpDo(t, app, httpUnsafeRequest("POST", "/", body, nil), tc.status)
			if tc.status == 204 {
				if got.OfferingID != 9223372036854775807 || got.AddressID == nil || *got.AddressID != 9223372036854775807 {
					t.Fatalf("int64 IDs lost precision: %+v", got)
				}
			} else {
				httpAssertSafeError(t, response, "invalid_request")
				if got.OfferingID != 0 {
					t.Fatal("overflow reached handler")
				}
			}
		})
	}
}
