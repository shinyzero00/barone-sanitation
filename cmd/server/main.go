package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/shinyzero00/barone-sanitation/internal/backend"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("command failed; check configuration, database and migration state")
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 || args[0] != "migrate" || args[1] != "up" && args[1] != "down" {
		fmt.Fprintln(os.Stderr, "usage: server migrate up|down")
		return errors.New("invalid arguments")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := backend.OpenDatabase(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()
	return migrations.Run(ctx, db.DB, args[1] == "down")
}
