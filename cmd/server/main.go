package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shinyzero00/barone-sanitation/internal/backend"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	mode := args[0]
	if mode != "serve" && mode != "migrate" && mode != "admin" || mode == "serve" && len(args) != 1 || mode == "migrate" && (len(args) != 2 || args[1] != "up" && args[1] != "down") || mode == "admin" && len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: server [serve | migrate up|down | admin EMAIL]")
		return errors.New("invalid arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("configuration: DATABASE_URL is required")
	}
	var cfg backend.Config
	var err error
	if mode == "serve" {
		cfg, err = backend.LoadConfig()
		if err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := backend.OpenDatabase(connectCtx, databaseURL)
	cancel()
	if err != nil {
		return commandFailure("database connection", "could not connect to PostgreSQL; check DATABASE_URL and server availability", err)
	}
	defer db.Close()
	if mode == "migrate" {
		migrationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := migrations.Run(migrationCtx, db.DB, args[1] == "down"); err != nil {
			return commandFailure("migrate "+args[1], "could not run migrations; check database permissions and migration state", err)
		}
		slog.Info("migrations completed")
		return nil
	}
	if mode == "admin" {
		adminCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		tx, err := db.BeginTxx(adminCtx, nil)
		if err != nil {
			return commandFailure("admin: begin transaction", "could not start database transaction", err)
		}
		defer tx.Rollback()
		var id int64
		if err = tx.GetContext(adminCtx, &id, `SELECT user_id FROM users WHERE lower(email)=$1 AND email_verified_at IS NOT NULL FOR UPDATE`, strings.ToLower(strings.TrimSpace(args[1]))); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return commandFailure("admin", "no verified account matches the supplied email", err)
			}
			return commandFailure("admin: look up verified account", "database query failed", err)
		}
		if _, err = tx.ExecContext(adminCtx, `UPDATE users SET role='admin' WHERE user_id=$1`, id); err != nil {
			return commandFailure("admin: update role", "database update failed", err)
		}
		if err = tx.Commit(); err != nil {
			return commandFailure("admin: commit", "could not commit role change", err)
		}
		slog.Info("verified account promoted to administrator")
		return nil
	}
	var otpProvider backend.OTPProvider = &backend.SMTPMailer{Config: cfg}
	if cfg.OTPProvider == "console" {
		otpProvider = &backend.ConsoleOTPProvider{Writer: os.Stdout}
	}
	s := &backend.Service{DB: db, Config: cfg, OTPProvider: otpProvider}
	app := backend.NewApp(s)
	done := make(chan error, 1)
	go func() { done <- app.Listen(cfg.ListenAddress) }()
	select {
	case err := <-done:
		return commandFailure("serve", "HTTP listener failed; check LISTEN_ADDRESS", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := app.ShutdownWithContext(shutdown); err != nil {
			return commandFailure("serve: shut down", "graceful shutdown failed", err)
		}
		return commandFailure("serve", "HTTP listener failed; check LISTEN_ADDRESS", <-done)
	}
}
