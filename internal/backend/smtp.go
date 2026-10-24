package backend

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer supports implicit TLS on port 465 and mandatory STARTTLS elsewhere.
// Construct it as &SMTPMailer{Config: config}; plaintext delivery is never allowed.
type SMTPMailer struct{ Config Config }

func (m *SMTPMailer) SendCode(ctx context.Context, email, purpose, code string) error {
	to, err := canonicalEmail(email)
	if err != nil {
		return fmt.Errorf("invalid mail recipient")
	}
	from, err := mail.ParseAddress(m.Config.SMTPFrom)
	if err != nil || strings.ContainsAny(m.Config.SMTPFrom, "\r\n") {
		return fmt.Errorf("invalid mail sender")
	}
	var subject string
	switch purpose {
	case "verify_email":
		subject = "Verify your email"
	case "login":
		subject = "Your login code"
	case "reset_password":
		subject = "Reset your password"
	default:
		return fmt.Errorf("invalid mail purpose")
	}
	if _, err = validateCode(CodeInput{Email: to, Code: code}); err != nil {
		return fmt.Errorf("invalid mail code")
	}
	host, port, err := net.SplitHostPort(m.Config.SMTPAddress)
	if err != nil || host == "" {
		return fmt.Errorf("invalid SMTP address")
	}
	if m.Config.SMTPTimeout <= 0 {
		return fmt.Errorf("invalid SMTP timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, m.Config.SMTPTimeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: m.Config.SMTPTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", m.Config.SMTPAddress)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var transport net.Conn = conn
	if port == "465" {
		tlsConn := tls.Client(conn, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		transport = tlsConn
	}
	client, err := smtp.NewClient(transport, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if port != "465" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server requires STARTTLS support")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete {
		return fmt.Errorf("SMTP TLS required")
	}
	if m.Config.SMTPUsername != "" || m.Config.SMTPPassword != "" {
		if m.Config.SMTPUsername == "" || m.Config.SMTPPassword == "" {
			return fmt.Errorf("incomplete SMTP credentials")
		}
		if err = client.Auth(smtp.PlainAuth("", m.Config.SMTPUsername, m.Config.SMTPPassword, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + from.String() + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour code is: " + code + "\r\n"
	if _, err = w.Write([]byte(message)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	// DATA was accepted; a failed QUIT must not revoke an already delivered code.
	_ = client.Quit()
	return nil
}
