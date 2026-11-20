package seed

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/shinyzero00/barone-sanitation/internal/backend"
)

//go:embed demo.sql assets/*.jpg
var demoFiles embed.FS

const demoMarker = "!barone-demo-no-login"

var ErrCatalogNotEmpty = errors.New("demo seed requires an empty consultation catalog")

func Demo(ctx context.Context, db *sqlx.DB) error {
	return runDemo(ctx, db, demoFiles)
}

func runDemo(ctx context.Context, db *sqlx.DB, files fs.FS) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(704184426, hashtext(current_schema()))`); err != nil {
		return err
	}
	var seeded, populated bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE password_hash=$1),
		EXISTS(SELECT 1 FROM consultation_types)`, demoMarker).Scan(&seeded, &populated); err != nil {
		return err
	}
	if !seeded {
		if populated {
			return ErrCatalogNotEmpty
		}
		script, err := fs.ReadFile(files, "demo.sql")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(script), demoMarker); err != nil {
			return err
		}
	}
	assets, err := fs.ReadDir(files, "assets")
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if asset.IsDir() || !strings.HasSuffix(asset.Name(), ".jpg") {
			continue
		}
		data, err := fs.ReadFile(files, "assets/"+asset.Name())
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		var id int64
		email := strings.TrimSuffix(asset.Name(), ".jpg") + "@barone.invalid"
		if err := tx.GetContext(ctx, &id, `SELECT consultant_id FROM consultants JOIN users USING(user_id)
			WHERE email=$1 AND password_hash=$2`, email, demoMarker); err != nil {
			return fmt.Errorf("find consultant for %s: %w", asset.Name(), err)
		}
		if err := (backend.BlobService{}).SetConsultantAvatar(ctx, tx, id, data, true); err != nil {
			return fmt.Errorf("import %s: %w", asset.Name(), err)
		}
	}
	return tx.Commit()
}
