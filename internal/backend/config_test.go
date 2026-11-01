package backend

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"
)

func configTestEnv(t *testing.T) {
	t.Helper()
	// Setenv restores the caller's environment; these tests must not run in parallel.
	for _, key := range []string{
		"DATABASE_URL", "LISTEN_ADDRESS", "PUBLIC_ORIGIN", "OTP_HMAC_SECRET",
		"OTP_TTL", "OTP_MAX_ATTEMPTS", "OTP_RESEND_INTERVAL", "OTP_LOCKOUT",
		"OTP_PROVIDER",
		"SESSION_TTL", "REQUEST_TIMEOUT", "SMTP_TIMEOUT", "RATE_WINDOW",
		"RATE_LIMIT", "SLOT_STEP", "TRUSTED_PROXIES", "SMTP_ADDRESS",
		"SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://config@test.invalid/config")
	t.Setenv("OTP_HMAC_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	t.Setenv("SMTP_ADDRESS", "smtp.test.invalid:587")
	t.Setenv("SMTP_FROM", "sender@test.invalid")
}

func TestLoadConfigDefaults(t *testing.T) {
	configTestEnv(t)
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://config@test.invalid/config", ListenAddress: "127.0.0.1:8080",
		PublicOrigin: "https://localhost:8080", HMACSecret: []byte(strings.Repeat("s", 32)),
		OTPProvider: "smtp",
		CodeTTL:     10 * time.Minute, MaxAttempts: 5, ResendInterval: time.Minute,
		Lockout: 15 * time.Minute, SessionTTL: 720 * time.Hour,
		RequestTimeout: 10 * time.Second, SMTPTimeout: 10 * time.Second,
		RateWindow: time.Minute, RateLimit: 30, SlotStep: 15 * time.Minute,
		SMTPAddress: "smtp.test.invalid:587", SMTPFrom: "sender@test.invalid",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, want)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	configTestEnv(t)
	for key, value := range map[string]string{
		"DATABASE_URL": "postgres://other@test.invalid/other", "LISTEN_ADDRESS": "127.0.0.1:9090",
		"PUBLIC_ORIGIN":   "https://app.test.invalid:8443",
		"OTP_HMAC_SECRET": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 48))),
		"OTP_TTL":         "2m", "OTP_MAX_ATTEMPTS": "1", "OTP_RESEND_INTERVAL": "3s",
		"OTP_LOCKOUT": "4m", "SESSION_TTL": "5h", "REQUEST_TIMEOUT": "6s",
		"OTP_PROVIDER": "smtp",
		"SMTP_TIMEOUT": "7s", "RATE_WINDOW": "8m", "RATE_LIMIT": "9", "SLOT_STEP": "10m",
		"TRUSTED_PROXIES": " 127.0.0.1, 10.0.0.0/8 , ::1, 2001:db8::/32 ",
		"SMTP_ADDRESS":    "[::1]:465", "SMTP_FROM": "Sender <sender@test.invalid>",
		"SMTP_USERNAME": "user", "SMTP_PASSWORD": "password",
	} {
		t.Setenv(key, value)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://other@test.invalid/other", ListenAddress: "127.0.0.1:9090",
		PublicOrigin: "https://app.test.invalid:8443", HMACSecret: []byte(strings.Repeat("k", 48)),
		OTPProvider: "smtp",
		CodeTTL:     2 * time.Minute, MaxAttempts: 1, ResendInterval: 3 * time.Second,
		Lockout: 4 * time.Minute, SessionTTL: 5 * time.Hour,
		RequestTimeout: 6 * time.Second, SMTPTimeout: 7 * time.Second,
		RateWindow: 8 * time.Minute, RateLimit: 9, SlotStep: 10 * time.Minute,
		TrustedProxies: []string{"127.0.0.1", "10.0.0.0/8", "::1", "2001:db8::/32"},
		SMTPAddress:    "[::1]:465", SMTPFrom: "Sender <sender@test.invalid>",
		SMTPUsername: "user", SMTPPassword: "password",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, want)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	for _, tt := range []struct {
		name, key, value, want string
	}{
		{"database_required", "DATABASE_URL", "", "DATABASE_URL is required"},
		{"secret_empty", "OTP_HMAC_SECRET", "", "OTP_HMAC_SECRET"},
		{"secret_malformed", "OTP_HMAC_SECRET", "not base64!", "OTP_HMAC_SECRET"},
		{"secret_short", "OTP_HMAC_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 31))), "OTP_HMAC_SECRET"},
		{"origin_http", "PUBLIC_ORIGIN", "http://app.test.invalid", "PUBLIC_ORIGIN"},
		{"origin_path", "PUBLIC_ORIGIN", "https://app.test.invalid/api", "PUBLIC_ORIGIN"},
		{"origin_trailing_slash", "PUBLIC_ORIGIN", "https://app.test.invalid/", "PUBLIC_ORIGIN"},
		{"origin_malformed", "PUBLIC_ORIGIN", "https://%zz", "PUBLIC_ORIGIN"},
		{"origin_no_host", "PUBLIC_ORIGIN", "https://", "PUBLIC_ORIGIN"},
		{"origin_query", "PUBLIC_ORIGIN", "https://app.test.invalid?key=value", "PUBLIC_ORIGIN"},
		{"origin_fragment", "PUBLIC_ORIGIN", "https://app.test.invalid#fragment", "PUBLIC_ORIGIN"},
		{"origin_credentials", "PUBLIC_ORIGIN", "https://user:pass@app.test.invalid", "PUBLIC_ORIGIN"},
		{"otp_provider_invalid", "OTP_PROVIDER", "stdout", "OTP_PROVIDER must be smtp or console"},
		{"proxy_invalid_ip", "TRUSTED_PROXIES", "999.0.0.1", "invalid trusted proxy"},
		{"proxy_invalid_cidr", "TRUSTED_PROXIES", "10.0.0.0/33", "invalid trusted proxy"},
		{"proxy_invalid_ipv6_cidr", "TRUSTED_PROXIES", "2001:db8::/129", "invalid trusted proxy"},
		{"proxy_hostname", "TRUSTED_PROXIES", "proxy.test.invalid", "invalid trusted proxy"},
		{"proxy_empty_item", "TRUSTED_PROXIES", "127.0.0.1,", "invalid trusted proxy"},
		{"proxy_whitespace", "TRUSTED_PROXIES", " ", "invalid trusted proxy"},
		{"smtp_address_required", "SMTP_ADDRESS", "", "SMTP_ADDRESS and SMTP_FROM are required"},
		{"smtp_from_required", "SMTP_FROM", "", "SMTP_ADDRESS and SMTP_FROM are required"},
		{"smtp_blank_host", "SMTP_ADDRESS", ":587", "invalid SMTP_ADDRESS"},
		{"smtp_missing_port", "SMTP_ADDRESS", "smtp.test.invalid", "invalid SMTP_ADDRESS"},
		{"smtp_blank_port", "SMTP_ADDRESS", "smtp.test.invalid:", "invalid SMTP_ADDRESS"},
		{"smtp_named_port", "SMTP_ADDRESS", "smtp.test.invalid:smtp", "invalid SMTP_ADDRESS"},
		{"smtp_zero_port", "SMTP_ADDRESS", "smtp.test.invalid:0", "invalid SMTP_ADDRESS"},
		{"smtp_negative_port", "SMTP_ADDRESS", "smtp.test.invalid:-1", "invalid SMTP_ADDRESS"},
		{"smtp_large_port", "SMTP_ADDRESS", "smtp.test.invalid:65536", "invalid SMTP_ADDRESS"},
		{"smtp_sender_malformed", "SMTP_FROM", "not a mailbox", "invalid SMTP_FROM"},
		{"smtp_sender_multiple", "SMTP_FROM", "one@test.invalid, two@test.invalid", "invalid SMTP_FROM"},
		{"smtp_sender_cr", "SMTP_FROM", "Sender\r <sender@test.invalid>", "invalid SMTP_FROM"},
		{"smtp_sender_lf", "SMTP_FROM", "Sender\n <sender@test.invalid>", "invalid SMTP_FROM"},
		{"smtp_username_only", "SMTP_USERNAME", "user", "SMTP_USERNAME and SMTP_PASSWORD must be set together"},
		{"smtp_password_only", "SMTP_PASSWORD", "password", "SMTP_USERNAME and SMTP_PASSWORD must be set together"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configTestEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
	for _, key := range []string{
		"OTP_TTL", "OTP_RESEND_INTERVAL", "OTP_LOCKOUT", "SESSION_TTL",
		"REQUEST_TIMEOUT", "SMTP_TIMEOUT", "RATE_WINDOW", "SLOT_STEP",
	} {
		for _, value := range []string{"invalid", "0s", "-1s", "999999999999999999999h"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				configTestEnv(t)
				t.Setenv(key, value)
				if _, err := LoadConfig(); err == nil || err.Error() != "invalid "+key {
					t.Fatalf("LoadConfig() error = %v, want invalid %s", err, key)
				}
			})
		}
	}
	for _, key := range []string{"OTP_MAX_ATTEMPTS", "RATE_LIMIT"} {
		for _, value := range []string{"invalid", "0", "-1", "1.5", "999999999999999999999"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				configTestEnv(t)
				t.Setenv(key, value)
				if _, err := LoadConfig(); err == nil || err.Error() != "invalid "+key {
					t.Fatalf("LoadConfig() error = %v, want invalid %s", err, key)
				}
			})
		}
	}
}

func TestLoadConfigConsoleOTPProvider(t *testing.T) {
	configTestEnv(t)
	t.Setenv("OTP_PROVIDER", "console")
	t.Setenv("SMTP_ADDRESS", "")
	t.Setenv("SMTP_FROM", "")
	t.Setenv("SMTP_USERNAME", "ignored")
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1")

	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.OTPProvider != "console" || !reflect.DeepEqual(got.TrustedProxies, []string{"127.0.0.1"}) {
		t.Fatalf("LoadConfig() = %+v", got)
	}
}
