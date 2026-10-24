package backend

import (
	"context"
	"database/sql"
	"errors"
)

type Profile struct {
	UserID int64  `db:"user_id" json:"user_id"`
	Name   string `db:"name" json:"name"`
	Email  string `db:"email" json:"email"`
	Role   string `db:"role" json:"role"`
	Phone  string `db:"phone" json:"phone"`
}

func (s *Service) GetProfile(ctx context.Context, actor Actor) (Profile, error) {
	var out Profile
	if actor.UserID <= 0 || actor.SessionID <= 0 {
		return out, problem(401, "unauthenticated")
	}
	err := s.DB.GetContext(ctx, &out, `SELECT u.user_id,u.name,u.email,u.role,COALESCE(c.phone,'') AS phone
		FROM users u JOIN sessions s USING(user_id) LEFT JOIN clients c USING(user_id)
		WHERE u.user_id=$1 AND s.session_id=$2 AND s.revoked_at IS NULL
		AND s.expires_at>clock_timestamp() AND u.email_verified_at IS NOT NULL`, actor.UserID, actor.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, problem(401, "unauthenticated")
	}
	return out, err
}
