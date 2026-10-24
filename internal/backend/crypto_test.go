package backend

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestAuthPasswords(t *testing.T) {
	password := "correct horse battery staple"
	a, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("password salts reused")
	}
	for _, tc := range []struct {
		name, hash, password string
		want                 bool
	}{
		{"correct", a, password, true},
		{"wrong", a, "incorrect horse battery staple", false},
		{"malformed", "bad", password, false},
		{"unbounded parameters", strings.Replace(a, "m=65536", "m=999999999", 1), password, false},
		{"short", a, "short", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkPassword(tc.hash, tc.password); got != tc.want {
				t.Fatalf("checkPassword = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthTokenCrypto(t *testing.T) {
	a, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(a))
	if a == b || !validToken(a) || !bytes.Equal(tokenHash(a), want[:]) || validToken(a+"=") {
		t.Fatal("invalid opaque token")
	}
}

func TestAuthInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name, email string
		valid       bool
	}{
		{"canonical", " Test@Example.COM ", true},
		{"display name", "Test <test@example.com>", false},
		{"injection", "test@example.com\r\nBcc: victim@example.com", false},
		{"missing domain", "test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email, err := canonicalEmail(tc.email)
			if (err == nil) != tc.valid {
				t.Fatalf("validation = %v", err)
			}
			if tc.valid && email != "test@example.com" {
				t.Fatal("email not canonicalized")
			}
		})
	}
	if validPassword(strings.Repeat("x", 129)) || validPassword("12345678901") || !validPassword(strings.Repeat("x", 128)) {
		t.Fatal("password byte bounds")
	}
}
