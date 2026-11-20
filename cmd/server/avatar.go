package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/shinyzero00/barone-sanitation/internal/backend"
)

func importAvatar(ctx context.Context, db *sqlx.DB, rawID, path string) error {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return errors.New("avatar: CONSULTANT_ID must be a positive integer")
	}
	file, err := os.Open(path)
	if err != nil {
		return commandFailure("avatar", "cannot open image file", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("avatar: input must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, backend.AvatarMaxBytes+1))
	if err != nil {
		return commandFailure("avatar", "cannot read image file", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return commandFailure("avatar", "could not begin import", err)
	}
	defer tx.Rollback()
	if err := (backend.BlobService{}).SetConsultantAvatar(ctx, tx, id, data, false); err != nil {
		return commandFailure("avatar", "consultant not found or invalid image (JPEG, PNG or WebP; up to 2 MiB and 2048 px per side)", err)
	}
	if err := tx.Commit(); err != nil {
		return commandFailure("avatar", "could not commit import", err)
	}
	slog.Info("consultant avatar imported", "consultant_id", id)
	return nil
}
