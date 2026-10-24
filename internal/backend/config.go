package backend

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/mail"
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
	CodeTTL        time.Duration
	MaxAttempts    int
	ResendInterval time.Duration
	Lockout        time.Duration
	SessionTTL     time.Duration
	RequestTimeout time.Duration
	SMTPTimeout    time.Duration
	RateWindow     time.Duration
	RateLimit      int
	SlotStep       time.Duration
	TrustedProxies []string
	SMTPAddress    string
	SMTPUsername   string
	SMTPPassword   string
	SMTPFrom       string
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), ListenAddress: env("LISTEN_ADDRESS", "127.0.0.1:8080"),
		PublicOrigin: env("PUBLIC_ORIGIN", "https://localhost:8080"),
		SMTPAddress:  os.Getenv("SMTP_ADDRESS"), SMTPUsername: os.Getenv("SMTP_USERNAME"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"), SMTPFrom: os.Getenv("SMTP_FROM"),
	}
	var err error
	c.HMACSecret, err = base64.StdEncoding.DecodeString(os.Getenv("OTP_HMAC_SECRET"))
	if err != nil || len(c.HMACSecret) < 32 {
		return c, fmt.Errorf("OTP_HMAC_SECRET must be base64 encoding of at least 32 random bytes")
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
		{"OTP_TTL", "10m", &c.CodeTTL}, {"OTP_RESEND_INTERVAL", "1m", &c.ResendInterval},
		{"OTP_LOCKOUT", "15m", &c.Lockout}, {"SESSION_TTL", "720h", &c.SessionTTL},
		{"REQUEST_TIMEOUT", "10s", &c.RequestTimeout}, {"SMTP_TIMEOUT", "10s", &c.SMTPTimeout},
		{"RATE_WINDOW", "1m", &c.RateWindow}, {"SLOT_STEP", "15m", &c.SlotStep},
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
		{"OTP_MAX_ATTEMPTS", "5", &c.MaxAttempts}, {"RATE_LIMIT", "30", &c.RateLimit},
	} {
		*item.dst, err = strconv.Atoi(env(item.key, item.value))
		if err != nil || *item.dst < 1 {
			return c, fmt.Errorf("invalid %s", item.key)
		}
	}
	if c.SMTPAddress == "" || c.SMTPFrom == "" {
		return c, fmt.Errorf("SMTP_ADDRESS and SMTP_FROM are required")
	}
	host, port, err := net.SplitHostPort(c.SMTPAddress)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return c, fmt.Errorf("invalid SMTP_ADDRESS")
	}
	if _, err := mail.ParseAddress(c.SMTPFrom); err != nil || strings.ContainsAny(c.SMTPFrom, "\r\n") {
		return c, fmt.Errorf("invalid SMTP_FROM")
	}
	if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
		return c, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must be set together")
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
