package backend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

const (
	httpBodyLimit = 16 * 1024
	sessionCookie = "__Host-session"
	csrfCookie    = "__Host-csrf"
)

type httpLocal uint8

const (
	httpActorKey httpLocal = iota
	httpIPKey
)

// NewApp exposes the service through a same-origin, cookie-authenticated API.
func NewApp(s *Service) *fiber.App {
	timeout := s.Config.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	app := fiber.New(fiber.Config{
		BodyLimit: httpBodyLimit, ReadTimeout: timeout, WriteTimeout: timeout,
		ErrorHandler: func(c fiber.Ctx, err error) error {
			httpSecurityHeaders(c, s.Config.PublicOrigin)
			return httpError(c, err)
		},
		// ProxyHeader is deliberately unset; httpClientIP validates the entire trust boundary.
	})
	app.Use(func(c fiber.Ctx) error {
		httpSecurityHeaders(c, s.Config.PublicOrigin)
		ctx, cancel := context.WithTimeout(c.Context(), timeout)
		defer cancel()
		c.SetContext(ctx)
		return c.Next()
	})
	app.Use(recover.New(recover.Config{PanicHandler: func(fiber.Ctx, any) error {
		return errors.New("http panic")
	}}))
	app.Use(func(c fiber.Ctx) error {
		if httpUnsafe(c.Method()) {
			if s.Config.PublicOrigin == "" || c.Get("Origin") != s.Config.PublicOrigin {
				return problem(403, "invalid_origin")
			}
			cookie, token := c.Cookies(csrfCookie), c.Get("X-CSRF-Token")
			if !httpValidCSRF(s.Config.HMACSecret, cookie) || subtle.ConstantTimeCompare([]byte(cookie), []byte(token)) != 1 {
				return problem(403, "invalid_csrf")
			}
			if len(c.Body()) > 0 {
				mediaType, _, err := mime.ParseMediaType(c.Get("Content-Type"))
				if err != nil || mediaType != "application/json" {
					return problem(415, "application_json_required")
				}
			}
		}
		return c.Next()
	})
	app.Get("/healthz", func(c fiber.Ctx) error {
		if err := s.DB.PingContext(c.Context()); err != nil {
			return problem(503, "unhealthy")
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})
	app.Use("/api/v1", func(c fiber.Ctx) error {
		ip := httpClientIP(c.RequestCtx().RemoteIP(), c.Get("X-Forwarded-For"), s.Config.TrustedProxies)
		c.Locals(httpIPKey, ip)
		if err := s.RateLimit(c.Context(), ip, "http"); err != nil {
			return err
		}
		return c.Next()
	})
	csrf := func(c fiber.Ctx) error {
		if len(s.Config.HMACSecret) < 32 {
			return problem(503, "authentication_unavailable")
		}
		token := c.Cookies(csrfCookie)
		if !httpValidCSRF(s.Config.HMACSecret, token) {
			random, err := newToken()
			if err != nil {
				return err
			}
			token = random + "." + httpCSRFSignature(s.Config.HMACSecret, random)
			httpSetCookie(c, csrfCookie, token, 24*time.Hour, false)
		}
		return c.JSON(fiber.Map{"csrf_token": token})
	}
	app.Get("/csrf", csrf)
	app.Get("/api/v1/auth/csrf", csrf)

	// Every protected endpoint authenticates anew.
	add := func(method, path string, protected bool, status int, fn func(fiber.Ctx) (any, error)) {
		app.Add([]string{method}, "/api/v1"+path, func(c fiber.Ctx) error {
			if protected {
				actor, err := s.Authenticate(c.Context(), c.Cookies(sessionCookie))
				if err != nil {
					return err
				}
				c.Locals(httpActorKey, actor)
			}
			if path == "/auth/logout" && len(c.Body()) > 0 {
				if err := httpDecode(c, &struct{}{}); err != nil {
					return err
				}
			}
			out, err := fn(c)
			if err != nil {
				return err
			}
			if status == 204 {
				return c.SendStatus(204)
			}
			return c.Status(status).JSON(out)
		})
	}
	add("POST", "/auth/register", false, 201, httpJSON(func(c fiber.Ctx, in RegisterInput) (any, error) {
		id, err := s.Register(c.Context(), in, httpIP(c))
		return fiber.Map{"user_id": id}, err
	}))
	add("POST", "/auth/login", false, 204, httpJSON(func(c fiber.Ctx, in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}) (any, error) {
		token, err := s.Login(c.Context(), in.Email, in.Password, httpIP(c))
		if err == nil {
			httpSetCookie(c, sessionCookie, token, s.Config.SessionTTL, true)
		}
		return nil, err
	}))
	add("POST", "/auth/logout", true, 204, func(c fiber.Ctx) (any, error) {
		err := s.Logout(c.Context(), httpActor(c))
		if err == nil {
			httpSetCookie(c, sessionCookie, "", -time.Second, true)
		}
		return nil, err
	})
	add("GET", "/me", true, 200, func(c fiber.Ctx) (any, error) { return s.GetProfile(c.Context(), httpActor(c)) })
	app.Use(func(fiber.Ctx) error { return problem(404, "not_found") })
	return app
}

func httpUnsafe(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}
func httpActor(c fiber.Ctx) Actor { return c.Locals(httpActorKey).(Actor) }
func httpIP(c fiber.Ctx) string   { return c.Locals(httpIPKey).(string) }

func httpJSON[T any](fn func(fiber.Ctx, T) (any, error)) func(fiber.Ctx) (any, error) {
	return func(c fiber.Ctx) (any, error) {
		var in T
		if err := httpDecode(c, &in); err != nil {
			return nil, err
		}
		return fn(c, in)
	}
}

func httpDecode(c fiber.Ctx, out any) error {
	mediaType, _, err := mime.ParseMediaType(c.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return problem(415, "application_json_required")
	}
	body := bytes.TrimSpace(c.Body())
	if len(body) > httpBodyLimit {
		return problem(413, "request_too_large")
	}
	if len(body) == 0 || body[0] != '{' {
		return problem(400, "invalid_json")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return problem(400, "invalid_json")
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return problem(400, "invalid_json")
	}
	return nil
}

func httpSetCookie(c fiber.Ctx, name, value string, ttl time.Duration, httpOnly bool) {
	c.Cookie(&fiber.Cookie{Name: name, Value: value, Path: "/", Secure: true, HTTPOnly: httpOnly,
		SameSite: "Strict", MaxAge: int(math.Ceil(ttl.Seconds())), Expires: time.Now().Add(ttl)})
}

func httpCSRFSignature(secret []byte, token string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("http-csrf:" + token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func httpValidCSRF(secret []byte, token string) bool {
	random, signature, ok := strings.Cut(token, ".")
	return len(secret) >= 32 && ok && validToken(random) && hmac.Equal([]byte(signature), []byte(httpCSRFSignature(secret, random)))
}

// Ignore forged left-hand entries: only proxies to the right of the first untrusted hop are authoritative.
func httpClientIP(peer net.IP, forwarded string, proxies []string) string {
	trusted := func(ip net.IP) bool {
		for _, proxy := range proxies {
			if candidate := net.ParseIP(proxy); candidate != nil && candidate.Equal(ip) {
				return true
			}
			if _, network, err := net.ParseCIDR(proxy); err == nil && network.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) || forwarded == "" {
		return peer.String()
	}
	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		// Malformed chains fail closed to the immediate peer, never a claimed leftmost address.
		if ip == nil {
			return peer.String()
		}
		if !trusted(ip) {
			return ip.String()
		}
	}
	return peer.String()
}

func httpSecurityHeaders(c fiber.Ctx, origin string) {
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(origin, "https://") {
		c.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func httpError(c fiber.Ctx, err error) error {
	status, code := 500, "internal_error"
	var api *APIError
	var framework *fiber.Error
	if errors.As(err, &api) && api.Status >= 400 && api.Status < 600 {
		status, code = api.Status, api.Code
		if api.RetryAfter > 0 {
			c.Set("Retry-After", strconv.FormatInt(int64(math.Ceil(api.RetryAfter.Seconds())), 10))
		}
	} else if errors.As(err, &framework) {
		switch framework.Code {
		case 400:
			status, code = 400, "bad_request"
		case 404:
			status, code = 404, "not_found"
		case 405:
			status, code = 405, "method_not_allowed"
		case 413:
			status, code = 413, "request_too_large"
		}
	}
	if status == 500 {
		slog.Error("http_request_failed")
	}
	// Server-level parsing errors can occur before middleware executes.
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("X-Frame-Options", "DENY")
	return c.Status(status).JSON(fiber.Map{"error": code})
}
