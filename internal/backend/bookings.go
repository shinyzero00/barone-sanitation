package backend

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type Booking struct {
	ID                int64     `db:"consultation_booking_id" json:"consultation_booking_id"`
	ClientID          int64     `db:"client_id" json:"client_id"`
	OfferingID        int64     `db:"consultation_offering_id" json:"consultation_offering_id"`
	AddressID         *int64    `db:"client_address_id" json:"client_address_id"`
	ConsultantID      int64     `db:"consultant_id" json:"consultant_id"`
	TypeID            int64     `db:"consultation_type_id" json:"consultation_type_id"`
	StartsAt          time.Time `db:"starts_at" json:"starts_at"`
	EndsAt            time.Time `db:"ends_at" json:"ends_at"`
	CreatedAt         time.Time `db:"created_at" json:"created_at"`
	Status            string    `db:"status" json:"status"`
	Price             string    `db:"price" json:"price"`
	DurationMinutes   int       `db:"duration_minutes" json:"duration_minutes"`
	ConsultationTitle string    `db:"consultation_title" json:"consultation_title"`
	ConsultantName    string    `db:"consultant_name" json:"consultant_name"`
	ClientName        string    `db:"client_name" json:"client_name"`
	ClientAvatarURL   *string   `db:"client_avatar_url" json:"client_avatar_url"`
	AddressLine       *string   `db:"address_line" json:"address_line" doc:"Current address text; null if no address was selected."`
}

type BookingInput struct {
	OfferingID int64     `json:"consultation_offering_id" minimum:"1"`
	AddressID  *int64    `json:"client_address_id" required:"false" minimum:"1"`
	StartsAt   time.Time `json:"starts_at"`
}

const bookingSelect = `SELECT b.consultation_booking_id,b.client_id,b.consultation_offering_id,b.client_address_id,
	o.consultant_id,o.consultation_type_id,b.starts_at,b.ends_at,b.created_at,s.status,t.price::text AS price,t.duration_minutes,
	t.title AS consultation_title,ku.name AS consultant_name,cu.name AS client_name,a.address_line,
	CASE WHEN c.user_id=$1 OR $2='admin' THEN '/api/v1/blobs/' || c.avatar_blob_id END AS client_avatar_url
	FROM consultation_bookings b JOIN consultation_offerings o USING(consultation_offering_id)
	JOIN consultation_types t USING(consultation_type_id) JOIN clients c USING(client_id)
	JOIN consultants k ON k.consultant_id=o.consultant_id
	JOIN users cu ON cu.user_id=c.user_id JOIN users ku ON ku.user_id=k.user_id
	LEFT JOIN client_addresses a ON a.client_id=b.client_id AND a.client_address_id=b.client_address_id
	JOIN consultation_booking_current_statuses s USING(consultation_booking_id)`

const bookingScope = ` ($2='admin' OR c.user_id=$1 OR ($2='consultant' AND k.user_id=$1))`

func (s *Service) GetBooking(ctx context.Context, actor Actor, id int64) (Booking, error) {
	var b Booking
	if id <= 0 {
		return b, problem(422, "invalid_id")
	}
	role, err := domainActor(ctx, s.DB, actor, false)
	if err != nil {
		return b, err
	}
	err = s.DB.GetContext(ctx, &b, bookingSelect+` WHERE `+bookingScope+` AND b.consultation_booking_id=$3`, actor.UserID, role, id)
	return b, domainError(err)
}

// ListBookings uses a bounded start-time range and returns at most 1000 rows per page.
func (s *Service) ListBookings(ctx context.Context, actor Actor, from, to time.Time, limit, offset int) ([]Booking, error) {
	result := []Booking{}
	if from.IsZero() || !to.After(from) || to.Sub(from) > 366*24*time.Hour || limit < 1 || limit > 1000 || offset < 0 {
		return nil, problem(422, "invalid_range")
	}
	role, err := domainActor(ctx, s.DB, actor, false)
	if err != nil {
		return nil, err
	}
	err = s.DB.SelectContext(ctx, &result, bookingSelect+` WHERE `+bookingScope+` AND b.starts_at >= $3 AND b.starts_at < $4 ORDER BY b.starts_at,b.consultation_booking_id LIMIT $5 OFFSET $6`, actor.UserID, role, from, to, limit, offset)
	return result, domainError(err)
}

func (s *Service) CreateBooking(ctx context.Context, actor Actor, in BookingInput) (Booking, error) {
	var result Booking
	if in.OfferingID <= 0 || in.AddressID != nil && *in.AddressID <= 0 || in.StartsAt.IsZero() {
		return result, problem(422, "invalid_booking")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "client" {
			return problem(403, "forbidden")
		}
		var o Offering
		if err := tx.GetContext(ctx, &o, `SELECT consultation_offering_id,consultant_id,consultation_type_id,is_active FROM consultation_offerings WHERE consultation_offering_id=$1`, in.OfferingID); err != nil {
			return err
		}
		var typeID int64
		if err := tx.GetContext(ctx, &typeID, `SELECT consultation_type_id FROM consultation_types WHERE consultation_type_id=$1 FOR UPDATE`, o.TypeID); err != nil {
			return err
		}
		if err := lockConsultant(ctx, tx, o.ConsultantID); err != nil {
			return err
		}
		var active bool
		if err := tx.GetContext(ctx, &active, `SELECT o.is_active AND t.is_active AND c.is_active FROM consultation_offerings o JOIN consultation_types t USING(consultation_type_id) JOIN consultants c USING(consultant_id) WHERE consultation_offering_id=$1`, in.OfferingID); err != nil {
			return err
		}
		if !active {
			return problem(409, "inactive_offering")
		}
		var clientID int64
		if err := tx.GetContext(ctx, &clientID, `SELECT client_id FROM clients WHERE user_id=$1`, actor.UserID); err != nil {
			return err
		}
		if in.AddressID != nil {
			var owned bool
			if err := tx.GetContext(ctx, &owned, `SELECT EXISTS(SELECT 1 FROM client_addresses WHERE client_address_id=$1 AND client_id=$2)`, *in.AddressID, clientID); err != nil {
				return err
			}
			if !owned {
				return problem(422, "invalid_address")
			}
		}
		var end time.Time
		var future bool
		if err := tx.QueryRowxContext(ctx, `SELECT $1::timestamptz+duration_minutes*interval '1 minute',$1::timestamptz>clock_timestamp() FROM consultation_types WHERE consultation_type_id=$2`, in.StartsAt, o.TypeID).Scan(&end, &future); err != nil {
			return err
		}
		if !future {
			return problem(422, "booking_in_past")
		}
		var fits, overlap bool
		if err := tx.GetContext(ctx, &fits, `SELECT EXISTS(SELECT 1 FROM work_intervals WHERE consultant_id=$1 AND starts_at<=$2 AND ends_at>=$3)`, o.ConsultantID, in.StartsAt, end); err != nil {
			return err
		}
		if !fits {
			return problem(409, "outside_work_interval")
		}
		if err := tx.GetContext(ctx, &overlap, `SELECT EXISTS(SELECT 1 FROM consultation_bookings b JOIN consultation_offerings o USING(consultation_offering_id) JOIN consultation_booking_current_statuses s USING(consultation_booking_id) WHERE o.consultant_id=$1 AND s.status IN ('pending','confirmed') AND b.starts_at<$3 AND b.ends_at>$2)`, o.ConsultantID, in.StartsAt, end); err != nil {
			return err
		}
		if overlap {
			return problem(409, "booking_overlap")
		}
		var id int64
		if err := tx.GetContext(ctx, &id, `INSERT INTO consultation_bookings(client_id,consultation_offering_id,client_address_id,starts_at,ends_at) VALUES($1,$2,$3,$4,$5) RETURNING consultation_booking_id`, clientID, in.OfferingID, in.AddressID, in.StartsAt, end); err != nil {
			return err
		}
		if err := domainAppendEvent(ctx, tx, id, actor.UserID, "pending", nil); err != nil {
			return err
		}
		return tx.GetContext(ctx, &result, bookingSelect+` WHERE b.consultation_booking_id=$3`, actor.UserID, role, id)
	})
	return result, err
}

type BookingEvent struct {
	ID         int64     `db:"consultation_booking_status_event_id" json:"consultation_booking_status_event_id"`
	BookingID  int64     `db:"consultation_booking_id" json:"consultation_booking_id"`
	Status     string    `db:"status" json:"status"`
	OccurredAt time.Time `db:"occurred_at" json:"occurred_at"`
	UserID     *int64    `db:"user_id" json:"user_id"`
	Reason     *string   `db:"reason" json:"reason"`
}

func domainAppendEvent(ctx context.Context, tx *sqlx.Tx, id, userID int64, status string, reason *string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO consultation_booking_status_events(consultation_booking_id,status,occurred_at,user_id,reason) VALUES($1,$2,GREATEST(clock_timestamp(),(SELECT max(occurred_at) FROM consultation_booking_status_events WHERE consultation_booking_id=$1)),$3,$4)`, id, status, userID, reason)
	return err
}

func (s *Service) ChangeBookingStatus(ctx context.Context, actor Actor, id int64, status string, reason *string) (Booking, error) {
	var result Booking
	if id <= 0 || reason != nil && !domainText(*reason, 0, 2000) {
		return result, problem(422, "invalid_status_change")
	}
	if status != "confirmed" && status != "cancelled" && status != "completed" && status != "no_show" {
		return result, problem(422, "invalid_status")
	}
	err := s.domainTx(ctx, actor, func(tx *sqlx.Tx, role string) error {
		if role != "admin" && role != "client" {
			return problem(403, "forbidden")
		}
		var consultantID int64
		if err := tx.GetContext(ctx, &consultantID, `SELECT o.consultant_id FROM consultation_bookings b JOIN consultation_offerings o USING(consultation_offering_id) JOIN clients c USING(client_id) WHERE b.consultation_booking_id=$1 AND ($2='admin' OR c.user_id=$3)`, id, role, actor.UserID); err != nil {
			return err
		}
		if role == "client" && status != "cancelled" {
			return problem(403, "forbidden")
		}
		if err := lockConsultant(ctx, tx, consultantID); err != nil {
			return err
		}
		var locked int64
		if err := tx.GetContext(ctx, &locked, `SELECT consultation_booking_id FROM consultation_bookings WHERE consultation_booking_id=$1 FOR UPDATE`, id); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &result, bookingSelect+` WHERE b.consultation_booking_id=$3`, actor.UserID, role, id); err != nil {
			return err
		}
		allowed := result.Status == "pending" && (status == "confirmed" || status == "cancelled") || result.Status == "confirmed" && (status == "completed" || status == "cancelled" || status == "no_show")
		if !allowed {
			return problem(409, "invalid_transition")
		}
		if err := domainAppendEvent(ctx, tx, id, actor.UserID, status, reason); err != nil {
			return err
		}
		result.Status = status
		return nil
	})
	return result, err
}

func (s *Service) ListBookingEvents(ctx context.Context, actor Actor, id int64) ([]BookingEvent, error) {
	result := []BookingEvent{}
	if _, err := s.GetBooking(ctx, actor, id); err != nil {
		return nil, err
	}
	err := s.DB.SelectContext(ctx, &result, `SELECT consultation_booking_status_event_id,consultation_booking_id,status,occurred_at,user_id,reason FROM consultation_booking_status_events WHERE consultation_booking_id=$1 ORDER BY occurred_at,consultation_booking_status_event_id`, id)
	return result, domainError(err)
}

type Slot struct {
	StartsAt time.Time `db:"starts_at" json:"starts_at"`
	EndsAt   time.Time `db:"ends_at" json:"ends_at"`
}

// AvailableSlots is advisory; CreateBooking rechecks availability under the consultant lock.
// The grid is anchored at from, and every result ends within [from,to].
func (s *Service) AvailableSlots(ctx context.Context, offeringID int64, from, to time.Time) ([]Slot, error) {
	result := []Slot{}
	if offeringID <= 0 || from.IsZero() || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return nil, problem(422, "invalid_range")
	}
	step := s.Config.SlotStep
	if step <= 0 {
		return nil, problem(500, "invalid_slot_step")
	}
	// Bound the number of generated candidates even with a misconfigured sub-second grid.
	if to.Sub(from)/step > 100000 {
		return nil, problem(422, "too_many_slots")
	}
	err := s.DB.SelectContext(ctx, &result, `SELECT g.starts_at,g.starts_at+t.duration_minutes*interval '1 minute' AS ends_at
	FROM consultation_offerings o JOIN consultation_types t USING(consultation_type_id) JOIN consultants c USING(consultant_id)
	CROSS JOIN LATERAL generate_series($2::timestamptz,$3::timestamptz, $4::double precision*interval '1 second') AS g(starts_at)
	WHERE o.consultation_offering_id=$1 AND o.is_active AND t.is_active AND c.is_active
	AND g.starts_at>clock_timestamp() AND g.starts_at+t.duration_minutes*interval '1 minute'<=$3
	AND EXISTS(SELECT 1 FROM work_intervals w WHERE w.consultant_id=o.consultant_id AND w.starts_at<=g.starts_at AND w.ends_at>=g.starts_at+t.duration_minutes*interval '1 minute')
	AND NOT EXISTS(SELECT 1 FROM consultation_bookings b JOIN consultation_offerings bo USING(consultation_offering_id) JOIN consultation_booking_current_statuses bs USING(consultation_booking_id) WHERE bo.consultant_id=o.consultant_id AND bs.status IN ('pending','confirmed') AND b.starts_at<g.starts_at+t.duration_minutes*interval '1 minute' AND b.ends_at>g.starts_at)
	ORDER BY g.starts_at`, offeringID, from, to, step.Seconds())
	return result, domainError(err)
}
