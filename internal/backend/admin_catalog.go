package backend

import (
	"context"

	"github.com/jmoiron/sqlx"
)

func (s *Service) requireAdmin(ctx context.Context, actor Actor) error {
	role, err := domainActor(ctx, s.DB, actor, false)
	if err == nil && role != "admin" {
		return problem(403, "forbidden")
	}
	return err
}

func adminList[T any](ctx context.Context, s *Service, actor Actor, query string, limit, offset int) ([]T, error) {
	if err := s.requireAdmin(ctx, actor); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 || offset < 0 {
		return nil, problem(422, "invalid_pagination")
	}
	rows := []T{}
	err := s.DB.SelectContext(ctx, &rows, query+` LIMIT $1 OFFSET $2`, limit, offset)
	return rows, domainError(err)
}

func (s *Service) ListAdminConsultants(ctx context.Context, actor Actor, limit, offset int) ([]Consultant, error) {
	return adminList[Consultant](ctx, s, actor, consultantSelect+` ORDER BY c.consultant_id`, limit, offset)
}

func (s *Service) ListAdminConsultationTypes(ctx context.Context, actor Actor, limit, offset int) ([]ConsultationType, error) {
	return adminList[ConsultationType](ctx, s, actor, `SELECT consultation_type_id,title,description,duration_minutes,price::text AS price,is_active FROM consultation_types ORDER BY consultation_type_id`, limit, offset)
}

func (s *Service) ListAdminOfferings(ctx context.Context, actor Actor, limit, offset int) ([]Offering, error) {
	return adminList[Offering](ctx, s, actor, `SELECT consultation_offering_id,consultant_id,consultation_type_id,is_active FROM consultation_offerings ORDER BY consultation_offering_id`, limit, offset)
}

type ConsultantCandidate struct {
	UserID int64  `db:"user_id" json:"user_id"`
	Name   string `db:"name" json:"name"`
}

func (s *Service) FindConsultantCandidate(ctx context.Context, actor Actor, email string) (ConsultantCandidate, error) {
	var candidate ConsultantCandidate
	if err := s.requireAdmin(ctx, actor); err != nil {
		return candidate, err
	}
	email, err := canonicalEmail(email)
	if err != nil {
		return candidate, err
	}
	err = s.DB.GetContext(ctx, &candidate, `SELECT user_id,name FROM users WHERE lower(email)=$1 AND role='client'`, email)
	return candidate, domainError(err)
}

func (s *Service) DeactivateConsultationType(ctx context.Context, actor Actor, id int64) error {
	return s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		var active bool
		if err := tx.GetContext(ctx, &active, `SELECT is_active FROM consultation_types WHERE consultation_type_id=$1 FOR UPDATE`, id); err != nil || !active {
			return err
		}
		if err := lockTypeConsultants(ctx, tx, id); err != nil {
			return err
		}
		return deactivateType(ctx, tx, id)
	})
}

func (s *Service) UpdateConsultantBio(ctx context.Context, actor Actor, id int64, bio string) error {
	if !domainText(bio, 0, 10000) {
		return problem(422, "invalid_consultant")
	}
	return s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" {
			return problem(403, "forbidden")
		}
		if err := lockConsultant(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE consultants SET bio=$2 WHERE consultant_id=$1`, id, bio)
		return err
	})
}
