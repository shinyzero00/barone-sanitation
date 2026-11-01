package backend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/shinyzero00/barone-sanitation/internal/apidocs"
)

const (
	httpBodyLimit   = 16 * 1024
	sessionCookie   = "__Host-session"
	challengeCookie = "__Host-login-challenge"
	csrfCookie      = "__Host-csrf"
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
				body := bytes.TrimSpace(c.Body())
				if len(body) == 0 || body[0] != '{' {
					return problem(400, "invalid_json")
				}
			}
		}
		return c.Next()
	})
	app.Use("/api/v1", func(c fiber.Ctx) error {
		ip := httpClientIP(c.RequestCtx().RemoteIP(), c.Get("X-Forwarded-For"), s.Config.TrustedProxies)
		c.SetContext(context.WithValue(c.Context(), httpIPKey, ip))
		if err := s.RateLimit(c.Context(), ip, "http"); err != nil {
			return err
		}
		return c.Next()
	})
	api := newHTTPAPI(app, s)
	registerHTTPOperations(api, s)
	apidocs.Register(app)
	app.Use(func(fiber.Ctx) error { return problem(404, "not_found") })
	return app
}

func httpUnsafe(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}
func httpActor(ctx context.Context) Actor { return ctx.Value(httpActorKey).(Actor) }
func httpIP(ctx context.Context) string   { return ctx.Value(httpIPKey).(string) }

func httpPositiveID(raw string) (int64, error) {
	if raw == "" {
		return 0, problem(422, "invalid_id")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, problem(422, "invalid_id")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, problem(422, "invalid_id")
	}
	return id, nil
}

func httpCookie(name, value string, ttl time.Duration, httpOnly bool) http.Cookie {
	maxAge := int(math.Ceil(ttl.Seconds()))
	if ttl < 0 {
		maxAge = -1
	}
	return http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: httpOnly,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: time.Now().Add(ttl)}
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
