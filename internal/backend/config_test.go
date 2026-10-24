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
	for _, key := range []string{
		"DATABASE_URL", "LISTEN_ADDRESS", "PUBLIC_ORIGIN", "CSRF_SECRET",
		"SESSION_TTL", "REQUEST_TIMEOUT", "RATE_WINDOW", "RATE_LIMIT", "TRUSTED_PROXIES",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://config@test.invalid/config")
	t.Setenv("CSRF_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
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
		SessionTTL: 720 * time.Hour, RequestTimeout: 10 * time.Second,
		RateWindow: time.Minute, RateLimit: 30,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, want)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	configTestEnv(t)
	for key, value := range map[string]string{
		"LISTEN_ADDRESS": "127.0.0.1:9090", "PUBLIC_ORIGIN": "https://app.test.invalid:8443",
		"CSRF_SECRET": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 48))),
		"SESSION_TTL": "5h", "REQUEST_TIMEOUT": "6s", "RATE_WINDOW": "8m", "RATE_LIMIT": "9",
		"TRUSTED_PROXIES": " 127.0.0.1, 10.0.0.0/8 , ::1, 2001:db8::/32 ",
	} {
		t.Setenv(key, value)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://config@test.invalid/config", ListenAddress: "127.0.0.1:9090",
		PublicOrigin: "https://app.test.invalid:8443", HMACSecret: []byte(strings.Repeat("k", 48)),
		SessionTTL: 5 * time.Hour, RequestTimeout: 6 * time.Second,
		RateWindow: 8 * time.Minute, RateLimit: 9,
		TrustedProxies: []string{"127.0.0.1", "10.0.0.0/8", "::1", "2001:db8::/32"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, want)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	for _, tt := range []struct {
		name, key, value, want string
	}{
		{"database_required", "DATABASE_URL", "", "DATABASE_URL"},
		{"secret_empty", "CSRF_SECRET", "", "CSRF_SECRET"},
		{"secret_malformed", "CSRF_SECRET", "not base64!", "CSRF_SECRET"},
		{"secret_short", "CSRF_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 31))), "CSRF_SECRET"},
		{"origin_http", "PUBLIC_ORIGIN", "http://app.test.invalid", "PUBLIC_ORIGIN"},
		{"origin_path", "PUBLIC_ORIGIN", "https://app.test.invalid/api", "PUBLIC_ORIGIN"},
		{"origin_query", "PUBLIC_ORIGIN", "https://app.test.invalid?key=value", "PUBLIC_ORIGIN"},
		{"origin_credentials", "PUBLIC_ORIGIN", "https://user:pass@app.test.invalid", "PUBLIC_ORIGIN"},
		{"proxy_hostname", "TRUSTED_PROXIES", "proxy.test.invalid", "invalid trusted proxy"},
		{"proxy_empty_item", "TRUSTED_PROXIES", "127.0.0.1,", "invalid trusted proxy"},
		{"proxy_invalid_cidr", "TRUSTED_PROXIES", "10.0.0.0/33", "invalid trusted proxy"},
		{"session_zero", "SESSION_TTL", "0s", "invalid SESSION_TTL"},
		{"timeout_negative", "REQUEST_TIMEOUT", "-1s", "invalid REQUEST_TIMEOUT"},
		{"window_malformed", "RATE_WINDOW", "invalid", "invalid RATE_WINDOW"},
		{"limit_zero", "RATE_LIMIT", "0", "invalid RATE_LIMIT"},
		{"limit_malformed", "RATE_LIMIT", "invalid", "invalid RATE_LIMIT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configTestEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
