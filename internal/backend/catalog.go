package backend

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

func domainError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return problem(404, "not_found")
	}
	return databaseError(err)
}

func domainText(s string, min, max int) bool {
	n := utf8.RuneCountInString(s)
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && n >= min && n <= max && (min == 0 || strings.TrimSpace(s) != "")
}

// Check the session in a fresh statement after locking, so revocation committed while waiting is visible.
func domainActor(ctx context.Context, q sqlx.QueryerContext, actor Actor, lock bool) (string, error) {
	if actor.UserID <= 0 || actor.SessionID <= 0 {
		return "", problem(401, "unauthorized")
	}
	if lock {
		var id int64
		if err := sqlx.GetContext(ctx, q, &id, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE`, actor.UserID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", problem(401, "unauthorized")
			}
			return "", domainError(err)
		}
	}
	var role string
	err := sqlx.GetContext(ctx, q, &role, `SELECT u.role FROM users u JOIN sessions s USING(user_id)
		WHERE u.user_id=$1 AND s.session_id=$2 AND u.email_verified_at IS NOT NULL
		AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`, actor.UserID, actor.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", problem(401, "unauthorized")
	}
	return role, domainError(err)
}

func (s *Service) domainTx(ctx context.Context, actor Actor, fn func(*sqlx.Tx, string) error, users ...int64) error {
	if actor.UserID <= 0 || actor.SessionID <= 0 {
		return problem(401, "unauthorized")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return databaseError(err)
	}
	defer tx.Rollback()
	if len(users) > 0 {
		ids := []int64{}
		if err := tx.SelectContext(ctx, &ids, `SELECT user_id FROM users WHERE user_id=ANY($1::bigint[]) ORDER BY user_id FOR UPDATE`, append(users, actor.UserID)); err != nil {
			return domainError(err)
		}
	}
	role, err := domainActor(ctx, tx, actor, true)
	if err != nil {
		return err
	}
	if err = fn(tx, role); err != nil {
		return domainError(err)
	}
	return databaseError(tx.Commit())
}

type Profile struct {
	UserID       int64   `db:"user_id" json:"user_id"`
	Name         string  `db:"name" json:"name"`
	Role         string  `db:"role" json:"role"`
	ClientID     *int64  `db:"client_id" json:"client_id"`
	Phone        *string `db:"phone" json:"phone"`
	ConsultantID *int64  `db:"consultant_id" json:"consultant_id"`
	Bio          *string `db:"bio" json:"bio"`
}

type ProfileUpdate struct {
	Name  *string `json:"name"`
	Phone *string `json:"phone"`
	Bio   *string `json:"bio"`
}

const profileSelect = `SELECT u.user_id,u.name,u.role,c.client_id,c.phone,k.consultant_id,k.bio
	FROM users u LEFT JOIN clients c USING(user_id) LEFT JOIN consultants k USING(user_id) WHERE u.user_id=$1`

func (s *Service) GetProfile(ctx context.Context, actor Actor) (Profile, error) {
	var p Profile
	if _, err := domainActor(ctx, s.DB, actor, false); err != nil {
		return p, err
	}
	err := s.DB.GetContext(ctx, &p, profileSelect, actor.UserID)
	return p, domainError(err)
}

func (s *Service) UpdateProfile(ctx context.Context, actor Actor, in ProfileUpdate) (Profile, error) {
	var p Profile
	if (in.Name != nil && !domainText(*in.Name, 1, 200)) || (in.Phone != nil && !domainText(*in.Phone, 0, 50)) || (in.Bio != nil && !domainText(*in.Bio, 0, 10000)) {
		return p, problem(422, "invalid_profile")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if err := tx.GetContext(ctx, &p, profileSelect, actor.UserID); err != nil {
			return err
		}
		if in.Phone != nil && p.ClientID == nil || in.Bio != nil && p.ConsultantID == nil {
			return problem(422, "profile_not_available")
		}
		if in.Name != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET name=$2 WHERE user_id=$1`, actor.UserID, *in.Name); err != nil {
				return err
			}
		}
		if in.Phone != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE clients SET phone=$2 WHERE user_id=$1`, actor.UserID, *in.Phone); err != nil {
				return err
			}
		}
		if in.Bio != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE consultants SET bio=$2 WHERE user_id=$1`, actor.UserID, *in.Bio); err != nil {
				return err
			}
		}
		return tx.GetContext(ctx, &p, profileSelect, actor.UserID)
	})
	return p, err
}

type Address struct {
	ID          int64   `db:"client_address_id" json:"client_address_id"`
	ClientID    int64   `db:"client_id" json:"client_id"`
	AddressLine string  `db:"address_line" json:"address_line"`
	Label       *string `db:"label" json:"label"`
}

type AddressInput struct {
	AddressLine string  `json:"address_line"`
	Label       *string `json:"label"`
}

func (s *Service) ListAddresses(ctx context.Context, actor Actor) ([]Address, error) {
	result := []Address{}
	if _, err := domainActor(ctx, s.DB, actor, false); err != nil {
		return nil, err
	}
	err := s.DB.SelectContext(ctx, &result, `SELECT a.client_address_id,a.client_id,a.address_line,a.label FROM client_addresses a JOIN clients c USING(client_id) WHERE c.user_id=$1 ORDER BY a.client_address_id`, actor.UserID)
	return result, domainError(err)
}

func (s *Service) GetAddress(ctx context.Context, actor Actor, id int64) (Address, error) {
	var a Address
	if id <= 0 {
		return a, problem(422, "invalid_id")
	}
	if _, err := domainActor(ctx, s.DB, actor, false); err != nil {
		return a, err
	}
	err := s.DB.GetContext(ctx, &a, `SELECT a.client_address_id,a.client_id,a.address_line,a.label FROM client_addresses a JOIN clients c USING(client_id) WHERE c.user_id=$1 AND a.client_address_id=$2`, actor.UserID, id)
	return a, domainError(err)
}

// id=0 creates; positive ids update an owned address.
func (s *Service) SaveAddress(ctx context.Context, actor Actor, id int64, in AddressInput) (Address, error) {
	var a Address
	if id < 0 || !domainText(in.AddressLine, 1, 1000) || in.Label != nil && !domainText(*in.Label, 0, 100) {
		return a, problem(422, "invalid_address")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if id == 0 {
			return tx.GetContext(ctx, &a, `INSERT INTO client_addresses(client_id,address_line,label) SELECT client_id,$2,$3 FROM clients WHERE user_id=$1 RETURNING client_address_id,client_id,address_line,label`, actor.UserID, in.AddressLine, in.Label)
		}
		return tx.GetContext(ctx, &a, `UPDATE client_addresses a SET address_line=$3,label=$4 FROM clients c WHERE a.client_id=c.client_id AND c.user_id=$1 AND a.client_address_id=$2 RETURNING a.client_address_id,a.client_id,a.address_line,a.label`, actor.UserID, id, in.AddressLine, in.Label)
	})
	return a, err
}

func (s *Service) DeleteAddress(ctx context.Context, actor Actor, id int64) error {
	if id <= 0 {
		return problem(422, "invalid_id")
	}
	return s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		var deleted int64
		return tx.GetContext(ctx, &deleted, `DELETE FROM client_addresses a USING clients c WHERE a.client_id=c.client_id AND c.user_id=$1 AND a.client_address_id=$2 RETURNING a.client_address_id`, actor.UserID, id)
	})
}

type Consultant struct {
	ID     int64   `db:"consultant_id" json:"consultant_id"`
	UserID int64   `db:"user_id" json:"user_id"`
	Name   string  `db:"name" json:"name"`
	Bio    *string `db:"bio" json:"bio"`
	Active bool    `db:"is_active" json:"is_active"`
}

func (s *Service) ListConsultants(ctx context.Context) ([]Consultant, error) {
	result := []Consultant{}
	err := s.DB.SelectContext(ctx, &result, `SELECT c.consultant_id,c.user_id,u.name,c.bio,c.is_active FROM consultants c JOIN users u USING(user_id) WHERE c.is_active ORDER BY c.consultant_id`)
	return result, domainError(err)
}

// ProvisionConsultant preserves any client profile and historical bookings, but changes the role to consultant.
func (s *Service) ProvisionConsultant(ctx context.Context, actor Actor, userID int64, bio string) (Consultant, error) {
	var c Consultant
	if userID <= 0 || !domainText(bio, 0, 10000) {
		return c, problem(422, "invalid_consultant")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		var id int64
		if err := tx.GetContext(ctx, &id, `SELECT user_id FROM users WHERE user_id=$1 AND role='client'`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role='consultant' WHERE user_id=$1`, userID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &c.ID, `INSERT INTO consultants(user_id,bio) VALUES($1,$2) RETURNING consultant_id`, userID, bio); err != nil {
			return err
		}
		return tx.GetContext(ctx, &c, `SELECT c.consultant_id,c.user_id,u.name,c.bio,c.is_active FROM consultants c JOIN users u USING(user_id) WHERE c.consultant_id=$1`, c.ID)
	}, userID)
	return c, err
}

func lockConsultant(ctx context.Context, tx *sqlx.Tx, id int64) error {
	var found int64
	return tx.GetContext(ctx, &found, `SELECT consultant_id FROM consultants WHERE consultant_id=$1 FOR UPDATE`, id)
}

func (s *Service) SetConsultantActive(ctx context.Context, actor Actor, id int64, active bool) error {
	if id <= 0 {
		return problem(422, "invalid_id")
	}
	return s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		if err := lockConsultant(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE consultants SET is_active=$2 WHERE consultant_id=$1`, id, active)
		return err
	})
}

type ConsultationType struct {
	ID              int64  `db:"consultation_type_id" json:"consultation_type_id"`
	Title           string `db:"title" json:"title"`
	Description     string `db:"description" json:"description"`
	DurationMinutes int    `db:"duration_minutes" json:"duration_minutes"`
	Price           string `db:"price" json:"price"`
	Active          bool   `db:"is_active" json:"is_active"`
}

type ConsultationTypeInput struct {
	Title           string `json:"title"`
	Description     string `json:"description"`
	DurationMinutes int    `json:"duration_minutes"`
	Price           string `json:"price"`
}

var domainPrice = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,2})?$`)

func (s *Service) ListConsultationTypes(ctx context.Context) ([]ConsultationType, error) {
	result := []ConsultationType{}
	err := s.DB.SelectContext(ctx, &result, `SELECT consultation_type_id,title,description,duration_minutes,price::text AS price,is_active FROM consultation_types WHERE is_active ORDER BY consultation_type_id`)
	return result, domainError(err)
}

// ReplaceConsultationType with oldID=0 creates a type. Commercial terms are never updated in place.
func (s *Service) ReplaceConsultationType(ctx context.Context, actor Actor, oldID int64, in ConsultationTypeInput) (ConsultationType, error) {
	var result ConsultationType
	if oldID < 0 || !domainText(in.Title, 1, 200) || !domainText(in.Description, 0, 10000) || in.DurationMinutes < 1 || in.DurationMinutes > 1440 || !domainPrice.MatchString(in.Price) {
		return result, problem(422, "invalid_consultation_type")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		if oldID > 0 {
			var active bool
			if err := tx.GetContext(ctx, &active, `SELECT is_active FROM consultation_types WHERE consultation_type_id=$1 FOR UPDATE`, oldID); err != nil {
				return err
			}
			if !active {
				return problem(409, "inactive_type")
			}
		}
		if err := tx.GetContext(ctx, &result, `INSERT INTO consultation_types(title,description,duration_minutes,price) VALUES($1,$2,$3,$4::numeric) RETURNING consultation_type_id,title,description,duration_minutes,price::text AS price,is_active`, in.Title, in.Description, in.DurationMinutes, in.Price); err != nil {
			return err
		}
		if oldID > 0 {
			// Assignment mutations hold the old type lock, so this set is stable.
			ids := []int64{}
			if err := tx.SelectContext(ctx, &ids, `SELECT c.consultant_id FROM consultants c WHERE EXISTS(SELECT 1 FROM consultation_offerings o WHERE o.consultant_id=c.consultant_id AND o.consultation_type_id=$1) ORDER BY c.consultant_id FOR UPDATE OF c`, oldID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO consultation_offerings(consultant_id,consultation_type_id) SELECT consultant_id,$2 FROM consultation_offerings WHERE consultation_type_id=$1 AND is_active`, oldID, result.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE consultation_offerings SET is_active=false WHERE consultation_type_id=$1`, oldID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE consultation_types SET is_active=false WHERE consultation_type_id=$1`, oldID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

type Offering struct {
	ID           int64 `db:"consultation_offering_id" json:"consultation_offering_id"`
	ConsultantID int64 `db:"consultant_id" json:"consultant_id"`
	TypeID       int64 `db:"consultation_type_id" json:"consultation_type_id"`
	Active       bool  `db:"is_active" json:"is_active"`
}

func (s *Service) ListOfferings(ctx context.Context) ([]Offering, error) {
	result := []Offering{}
	err := s.DB.SelectContext(ctx, &result, `SELECT o.consultation_offering_id,o.consultant_id,o.consultation_type_id,o.is_active FROM consultation_offerings o JOIN consultants c USING(consultant_id) JOIN consultation_types t USING(consultation_type_id) WHERE o.is_active AND c.is_active AND t.is_active ORDER BY o.consultation_offering_id`)
	return result, domainError(err)
}

func (s *Service) SetOffering(ctx context.Context, actor Actor, typeID, consultantID int64, active bool) (Offering, error) {
	var o Offering
	if typeID <= 0 || consultantID <= 0 {
		return o, problem(422, "invalid_id")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		var typeActive bool
		if err := tx.GetContext(ctx, &typeActive, `SELECT is_active FROM consultation_types WHERE consultation_type_id=$1 FOR UPDATE`, typeID); err != nil {
			return err
		}
		if active && !typeActive {
			return problem(409, "inactive_type")
		}
		if err := lockConsultant(ctx, tx, consultantID); err != nil {
			return err
		}
		return tx.GetContext(ctx, &o, `INSERT INTO consultation_offerings(consultation_type_id,consultant_id,is_active) VALUES($1,$2,$3) ON CONFLICT(consultant_id,consultation_type_id) DO UPDATE SET is_active=excluded.is_active RETURNING consultation_offering_id,consultant_id,consultation_type_id,is_active`, typeID, consultantID, active)
	})
	return o, err
}
