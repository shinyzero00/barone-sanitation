package backend

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/jmoiron/sqlx"
	_ "golang.org/x/image/webp"
)

const AvatarMaxBytes = 2 * 1024 * 1024

type BlobService struct{ Service *Service }

type avatarImage struct {
	ContentType string `db:"content_type"`
	Data        []byte `db:"data"`
}

func validateAvatar(data []byte) (string, error) {
	if len(data) > AvatarMaxBytes {
		return "", problem(413, "request_too_large")
	}
	const maxDimension = 2048
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxDimension || config.Height > maxDimension {
		return "", problem(422, "invalid_avatar")
	}
	if format != "jpeg" && format != "png" && format != "webp" {
		return "", problem(422, "invalid_avatar")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return "", problem(422, "invalid_avatar")
	}
	return "image/" + format, nil
}

func (s BlobService) get(ctx context.Context, id int64, actor Actor) (avatarImage, error) {
	var blob avatarImage
	err := s.Service.DB.GetContext(ctx, &blob, `SELECT b.content_type,b.data FROM blobs b WHERE b.blob_id=$1 AND (
		EXISTS(SELECT 1 FROM consultants k WHERE k.avatar_blob_id=b.blob_id AND k.is_active)
		OR EXISTS(SELECT 1 FROM clients c WHERE c.avatar_blob_id=b.blob_id AND (c.user_id=$2 OR $3='admin')))`, id, actor.UserID, actor.Role)
	return blob, domainError(err)
}

func (s BlobService) replaceClientAvatar(ctx context.Context, actor Actor, data []byte) error {
	var contentType string
	if data != nil {
		var err error
		contentType, err = validateAvatar(data)
		if err != nil {
			return err
		}
	}
	return s.Service.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "client" {
			return problem(403, "forbidden")
		}
		var old, next *int64
		if err := tx.GetContext(ctx, &old, `SELECT avatar_blob_id FROM clients WHERE user_id=$1 FOR UPDATE`, actor.UserID); err != nil {
			return err
		}
		if data != nil {
			var id int64
			if err := tx.GetContext(ctx, &id, `INSERT INTO blobs(content_type,data) VALUES($1,$2) RETURNING blob_id`, contentType, data); err != nil {
				return err
			}
			next = &id
		}
		if _, err := tx.ExecContext(ctx, `UPDATE clients SET avatar_blob_id=$2 WHERE user_id=$1`, actor.UserID, next); err != nil {
			return err
		}
		return deleteUnusedBlob(ctx, tx, old)
	})
}

func deleteUnusedBlob(ctx context.Context, tx *sqlx.Tx, id *int64) error {
	if id == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE blob_id=$1
		AND NOT EXISTS(SELECT 1 FROM clients WHERE avatar_blob_id=$1)
		AND NOT EXISTS(SELECT 1 FROM consultants WHERE avatar_blob_id=$1)`, *id)
	return err
}

// SetConsultantAvatar is used by trusted local imports within their transaction.
func (BlobService) SetConsultantAvatar(ctx context.Context, tx *sqlx.Tx, consultantID int64, data []byte, onlyIfMissing bool) error {
	contentType, err := validateAvatar(data)
	if err != nil {
		return err
	}
	var old *int64
	if err := tx.GetContext(ctx, &old, `SELECT avatar_blob_id FROM consultants WHERE consultant_id=$1 FOR UPDATE`, consultantID); err != nil {
		return err
	}
	if onlyIfMissing && old != nil {
		return nil
	}
	_, err = tx.ExecContext(ctx, `WITH inserted AS (INSERT INTO blobs(content_type,data) VALUES($2,$3) RETURNING blob_id)
		UPDATE consultants SET avatar_blob_id=inserted.blob_id FROM inserted WHERE consultant_id=$1`, consultantID, contentType, data)
	if err != nil {
		return err
	}
	return deleteUnusedBlob(ctx, tx, old)
}
