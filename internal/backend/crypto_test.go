package backend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
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

func TestAuthCodeAndTokenCrypto(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], 12)
	mac := hmac.New(sha256.New, secret)
	mac.Write(id[:])
	mac.Write([]byte("345678"))
	if !hmac.Equal(codeHash(secret, 12, "345678"), mac.Sum(nil)) {
		t.Fatal("hash does not use binary ID followed by code")
	}
	if hmac.Equal(codeHash(secret, 1, "234567"), codeHash(secret, 12, "34567")) {
		t.Fatal("ambiguous hash input")
	}
	if hmac.Equal(codeHash(secret, 12, "345678"), codeHash(bytes.Repeat([]byte{8}, 32), 12, "345678")) {
		t.Fatal("secret ignored")
	}
	for range 100 {
		code, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = validateCode(CodeInput{Email: "test@example.com", Code: code}); err != nil {
			t.Fatal("invalid random code")
		}
	}
	a, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !validToken(a) || len(tokenHash(a)) != 32 || validToken(a+"=") {
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
	if validPassword(strings.Repeat("я", 129)) || validPassword(strings.Repeat("я", 11)) || !validPassword(strings.Repeat("😀", 128)) {
		t.Fatal("password character bounds")
	}
}

func TestSMTPRejectsPlaintextAndInjection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = conn.Write([]byte("220 localhost SMTP\r\n"))
		if err != nil {
			done <- err
			return
		}
		buf := make([]byte, 1024)
		_, err = conn.Read(buf)
		if err == nil {
			_, err = conn.Write([]byte("250 localhost\r\n"))
		}
		done <- err
	}()
	m := &SMTPMailer{Config: Config{SMTPAddress: listener.Addr().String(), SMTPFrom: "sender@example.com", SMTPTimeout: time.Second, SMTPUsername: "user", SMTPPassword: "secret"}}
	if err = m.SendCode(context.Background(), "test@example.com", "login", "123456"); err == nil {
		t.Fatal("plaintext SMTP accepted")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = m.SendCode(context.Background(), "test@example.com\r\nBcc: x@example.com", "login", "123456"); err == nil {
		t.Fatal("recipient injection accepted")
	}
	if err = m.SendCode(context.Background(), "test@example.com", "login", "123456\r\n"); err == nil {
		t.Fatal("code injection accepted")
	}
	m.Config.SMTPFrom = "sender@example.com\r\nBcc: x@example.com"
	if err = m.SendCode(context.Background(), "test@example.com", "login", "123456"); err == nil {
		t.Fatal("sender injection accepted")
	}
}
