package backend

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type ConsultationResult struct {
	BookingID int64     `db:"consultation_booking_id" json:"consultation_booking_id"`
	Result    string    `db:"consultation_result" json:"consultation_result"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type ConsultationResultInput struct {
	Result string `json:"consultation_result" minLength:"1" maxLength:"100000"`
}

func (s *Service) GetConsultationResult(ctx context.Context, actor Actor, bookingID int64) (ConsultationResult, error) {
	var result ConsultationResult
	if bookingID <= 0 {
		return result, problem(422, "invalid_id")
	}
	if _, err := s.GetBooking(ctx, actor, bookingID); err != nil {
		return result, err
	}
	err := s.DB.GetContext(ctx, &result, `SELECT consultation_booking_id,consultation_result,created_at
		FROM consultation_results WHERE consultation_booking_id=$1`, bookingID)
	return result, domainError(err)
}

func (s *Service) CreateConsultationResult(ctx context.Context, actor Actor, bookingID int64, in ConsultationResultInput) (ConsultationResult, error) {
	var result ConsultationResult
	if bookingID <= 0 || !domainText(strings.TrimSpace(in.Result), 1, 100000) {
		return result, problem(422, "invalid_consultation_result")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "consultant" {
			return problem(403, "forbidden")
		}

		var consultantID int64
		if err := tx.GetContext(ctx, &consultantID, `SELECT o.consultant_id
			FROM consultation_bookings b JOIN consultation_offerings o USING(consultation_offering_id)
			WHERE b.consultation_booking_id=$1`, bookingID); err != nil {
			return err
		}
		if err := lockConsultant(ctx, tx, consultantID); err != nil {
			return err
		}

		var booking struct {
			ID             int64  `db:"consultation_booking_id"`
			ConsultantUser int64  `db:"consultant_user_id"`
			Status         string `db:"status"`
		}
		if err := tx.GetContext(ctx, &booking, `SELECT b.consultation_booking_id,u.user_id AS consultant_user_id,s.status
			FROM consultation_bookings b
			JOIN consultation_offerings o USING(consultation_offering_id)
			JOIN consultants c USING(consultant_id)
			JOIN users u USING(user_id)
			JOIN consultation_booking_current_statuses s USING(consultation_booking_id)
			WHERE b.consultation_booking_id=$1
			FOR UPDATE OF b`, bookingID); err != nil {
			return err
		}
		if booking.ConsultantUser != actor.UserID {
			return problem(403, "forbidden")
		}
		if booking.Status != "completed" {
			return problem(409, "booking_not_completed")
		}

		if err := tx.GetContext(ctx, &result, `SELECT consultation_booking_id,consultation_result,created_at
			FROM consultation_results WHERE consultation_booking_id=$1`, bookingID); err == nil {
			return problem(409, "consultation_result_exists")
		} else if err != sql.ErrNoRows {
			return err
		}
		if err := tx.GetContext(ctx, &result, `INSERT INTO consultation_results(consultation_booking_id,consultation_result)
			VALUES($1,$2) RETURNING consultation_booking_id,consultation_result,created_at`, bookingID, strings.TrimSpace(in.Result)); err != nil {
			return err
		}
		return nil
	})
	return result, err
}
