package backend

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL    string
	ListenAddress  string
	PublicOrigin   string
	HMACSecret     []byte
	SessionTTL     time.Duration
	RequestTimeout time.Duration
	RateWindow     time.Duration
	RateLimit      int
	TrustedProxies []string
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), ListenAddress: env("LISTEN_ADDRESS", "127.0.0.1:8080"),
		PublicOrigin: env("PUBLIC_ORIGIN", "https://localhost:8080"),
	}
	var err error
	c.HMACSecret, err = base64.StdEncoding.DecodeString(os.Getenv("CSRF_SECRET"))
	if err != nil || len(c.HMACSecret) < 32 {
		return c, fmt.Errorf("CSRF_SECRET must be base64 encoding of at least 32 random bytes")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return c, fmt.Errorf("PUBLIC_ORIGIN must be an HTTPS origin without a path")
	}
	for _, item := range []struct {
		key, value string
		dst        *time.Duration
	}{
		{"SESSION_TTL", "720h", &c.SessionTTL},
		{"REQUEST_TIMEOUT", "10s", &c.RequestTimeout},
		{"RATE_WINDOW", "1m", &c.RateWindow},
	} {
		*item.dst, err = time.ParseDuration(env(item.key, item.value))
		if err != nil || *item.dst <= 0 {
			return c, fmt.Errorf("invalid %s", item.key)
		}
	}
	for _, item := range []struct {
		key, value string
		dst        *int
	}{
		{"RATE_LIMIT", "30", &c.RateLimit},
	} {
		*item.dst, err = strconv.Atoi(env(item.key, item.value))
		if err != nil || *item.dst < 1 {
			return c, fmt.Errorf("invalid %s", item.key)
		}
	}
	if raw := os.Getenv("TRUSTED_PROXIES"); raw != "" {
		for _, proxy := range strings.Split(raw, ",") {
			proxy = strings.TrimSpace(proxy)
			if net.ParseIP(proxy) == nil {
				if _, _, err := net.ParseCIDR(proxy); err != nil {
					return c, fmt.Errorf("invalid trusted proxy")
				}
			}
			c.TrustedProxies = append(c.TrustedProxies, proxy)
		}
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
