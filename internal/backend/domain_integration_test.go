//go:build integration

package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

type domainFixture struct {
	s            *Service
	ctx          context.Context
	admin        Actor
	otherAdmin   Actor
	client       Actor
	otherClient  Actor
	consultant   Actor
	consultantID int64
	typ          ConsultationType
	offering     Offering
	interval     WorkInterval
	start        time.Time
}

func newDomainFixture(t *testing.T) *domainFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_TEST_URL")
	if dsn == "" {
		t.Skip("DATABASE_TEST_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root := sqlx.NewDb(stdlib.OpenDB(*cfg), "pgx")
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "domain_" + hex.EncodeToString(nonce[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = root.ExecContext(ctx, `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := root.ExecContext(cleanup, `DROP SCHEMA `+quoted+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	db := sqlx.NewDb(stdlib.OpenDB(*cfg), "pgx")
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = migrations.Run(ctx, db.DB, false); err != nil {
		t.Fatal(err)
	}
	f := &domainFixture{s: &Service{DB: db, Config: Config{SlotStep: 15 * time.Minute}}, ctx: ctx, start: time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)}
	userNumber := 0
	addUser := func(role string) Actor {
		var a Actor
		a.Role = role
		userNumber++
		if err := db.GetContext(ctx, &a.UserID, `INSERT INTO users(name,email,password_hash,role,email_verified_at) VALUES('Test',$1,'dummy',$2,clock_timestamp()) RETURNING user_id`, fmt.Sprintf("user%d@test.invalid", userNumber), role); err != nil {
			t.Fatal(err)
		}
		if role == "client" {
			if _, err := db.ExecContext(ctx, `INSERT INTO clients(user_id,phone) VALUES($1,'123')`, a.UserID); err != nil {
				t.Fatal(err)
			}
		}
		return f.session(t, a)
	}
	f.admin = addUser("admin")
	f.otherAdmin = addUser("admin")
	f.client = addUser("client")
	f.otherClient = addUser("client")
	f.consultant = addUser("client")
	c, err := f.s.ProvisionConsultant(ctx, f.admin, f.consultant.UserID, "Biography")
	if err != nil {
		t.Fatal(err)
	}
	f.consultantID = c.ID
	f.consultant.Role = "consultant"
	f.typ, err = f.s.ReplaceConsultationType(ctx, f.admin, 0, ConsultationTypeInput{Title: "Visit", DurationMinutes: 60, Price: "123.45"})
	if err != nil {
		t.Fatal(err)
	}
	f.offering, err = f.s.SetOffering(ctx, f.admin, f.typ.ID, c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	f.interval, err = f.s.SaveWorkInterval(ctx, f.consultant, c.ID, 0, WorkIntervalInput{StartsAt: f.start, EndsAt: f.start.Add(8 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *domainFixture) session(t *testing.T, actor Actor) Actor {
	t.Helper()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.GetContext(f.ctx, &actor.SessionID, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour') RETURNING session_id`, actor.UserID, token[:]); err != nil {
		t.Fatal(err)
	}
	return actor
}

func domainWantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Status != status {
		t.Fatalf("got error %v, want HTTP %d", err, status)
	}
}

func (f *domainFixture) book(t *testing.T, actor Actor, start time.Time) Booking {
	t.Helper()
	b, err := f.s.CreateBooking(f.ctx, actor, BookingInput{OfferingID: f.offering.ID, StartsAt: start})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func domainRace(first, second func() error) []error {
	start := make(chan struct{})
	result := make(chan error, 2)
	for _, fn := range []func() error{first, second} {
		go func() { <-start; result <- fn() }()
	}
	close(start)
	return []error{<-result, <-result}
}

func domainOneWinner(t *testing.T, errs []error) {
	t.Helper()
	winners := 0
	for _, err := range errs {
		if err == nil {
			winners++
		} else {
			domainWantStatus(t, err, 409)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d successes, want exactly one; errors=%v", winners, errs)
	}
}

func TestDomainBookingConcurrency(t *testing.T) {
	f := newDomainFixture(t)
	secondType, err := f.s.ReplaceConsultationType(f.ctx, f.admin, 0, ConsultationTypeInput{Title: "Other visit", DurationMinutes: 60, Price: "5"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.SetOffering(f.ctx, f.admin, secondType.ID, f.consultantID, true)
	if err != nil {
		t.Fatal(err)
	}
	errs := domainRace(func() error {
		_, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start})
		return err
	}, func() error {
		_, err := f.s.CreateBooking(f.ctx, f.otherClient, BookingInput{OfferingID: other.ID, StartsAt: f.start.Add(30 * time.Minute)})
		return err
	})
	domainOneWinner(t, errs)
	var starts, ends time.Time
	if err := f.s.DB.QueryRowxContext(f.ctx, `SELECT starts_at,ends_at FROM consultation_bookings`).Scan(&starts, &ends); err != nil {
		t.Fatal(err)
	}
	if ends.Sub(starts) != time.Hour {
		t.Fatalf("server duration = %s", ends.Sub(starts))
	}
	f.book(t, f.client, ends)
	events, err := f.s.ListBookingEvents(f.ctx, f.admin, 1)
	if err != nil || len(events) != 1 || events[0].Status != "pending" || events[0].UserID == nil {
		t.Fatalf("initial events: %+v, %v", events, err)
	}
	var count int
	if err := f.s.DB.GetContext(f.ctx, &count, `SELECT count(*) FROM consultation_bookings`); err != nil || count != 2 {
		t.Fatalf("booking count=%d: %v", count, err)
	}
}

func TestDomainOwnershipAndProfiles(t *testing.T) {
	f := newDomainFixture(t)
	name, phone, bio := "New name", "+1 555", "New bio"
	p, err := f.s.UpdateProfile(f.ctx, f.client, ProfileUpdate{Name: &name, Phone: &phone})
	if err != nil || p.Name != name || p.Phone == nil || *p.Phone != phone {
		t.Fatalf("profile: %+v %v", p, err)
	}
	p, err = f.s.UpdateProfile(f.ctx, f.consultant, ProfileUpdate{Bio: &bio})
	if err != nil || p.Bio == nil || *p.Bio != bio || p.ClientID == nil || p.Role != "consultant" {
		t.Fatalf("preserved client profile: %+v %v", p, err)
	}
	a, err := f.s.SaveAddress(f.ctx, f.client, 0, AddressInput{AddressLine: "123 Main"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.GetAddress(f.ctx, f.otherClient, a.ID)
	domainWantStatus(t, err, 404)
	_, err = f.s.SaveAddress(f.ctx, f.otherClient, a.ID, AddressInput{AddressLine: "Foreign edit"})
	domainWantStatus(t, err, 404)
	domainWantStatus(t, f.s.DeleteAddress(f.ctx, f.otherClient, a.ID), 404)
	_, err = f.s.CreateBooking(f.ctx, f.otherClient, BookingInput{OfferingID: f.offering.ID, AddressID: &a.ID, StartsAt: f.start})
	domainWantStatus(t, err, 422)
	b, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, AddressID: &a.ID, StartsAt: f.start})
	if err != nil {
		t.Fatal(err)
	}
	domainWantStatus(t, f.s.DeleteAddress(f.ctx, f.client, a.ID), 409)
	_, err = f.s.GetBooking(f.ctx, f.otherClient, b.ID)
	domainWantStatus(t, err, 404)
	_, err = f.s.ListBookingEvents(f.ctx, f.otherClient, b.ID)
	domainWantStatus(t, err, 404)
	_, err = f.s.ChangeBookingStatus(f.ctx, f.otherClient, b.ID, "cancelled", nil)
	domainWantStatus(t, err, 404)
	for _, actor := range []Actor{f.admin, f.consultant, f.client} {
		if _, err := f.s.GetBooking(f.ctx, actor, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	list, err := f.s.ListBookings(f.ctx, f.otherClient, f.start, f.start.Add(24*time.Hour), 100, 0)
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign list: %+v %v", list, err)
	}
	_, err = f.s.ChangeBookingStatus(f.ctx, f.consultant, b.ID, "confirmed", nil)
	domainWantStatus(t, err, 403)
	_, err = f.s.ChangeBookingStatus(f.ctx, f.client, b.ID, "confirmed", nil)
	domainWantStatus(t, err, 403)
	_, err = f.s.ChangeBookingStatus(f.ctx, f.client, b.ID, "cancelled", nil)
	if err != nil {
		t.Fatal(err)
	}
	domainWantStatus(t, f.s.DeleteAddress(f.ctx, f.client, a.ID), 409)
	_, err = f.s.CreateBooking(f.ctx, Actor{UserID: f.client.UserID, SessionID: f.client.SessionID, Role: "admin"}, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SetOffering(f.ctx, Actor{UserID: f.client.UserID, SessionID: f.client.SessionID, Role: "admin"}, f.typ.ID, f.consultantID, false)
	domainWantStatus(t, err, 403)
	_, err = f.s.CreateBooking(f.ctx, f.consultant, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start.Add(time.Hour)})
	domainWantStatus(t, err, 403)
	unused, err := f.s.SaveAddress(f.ctx, f.client, 0, AddressInput{AddressLine: "Unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteAddress(f.ctx, f.client, unused.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDomainImmutableCatalogReplacement(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	var userID int64
	if err := f.s.DB.GetContext(f.ctx, &userID, `INSERT INTO users(name,email,password_hash,role,email_verified_at) VALUES('Other','other@test.invalid','dummy','client',clock_timestamp()) RETURNING user_id`); err != nil {
		t.Fatal(err)
	}
	other, err := f.s.ProvisionConsultant(f.ctx, f.admin, userID, "")
	if err != nil {
		t.Fatal(err)
	}
	foreignConsultant := f.session(t, Actor{UserID: userID, Role: "consultant"})
	_, err = f.s.GetBooking(f.ctx, foreignConsultant, b.ID)
	domainWantStatus(t, err, 404)
	_, err = f.s.GetWorkInterval(f.ctx, foreignConsultant, f.consultantID, f.interval.ID)
	domainWantStatus(t, err, 404)
	_, err = f.s.SaveWorkInterval(f.ctx, foreignConsultant, f.consultantID, f.interval.ID, WorkIntervalInput{StartsAt: f.start, EndsAt: f.interval.EndsAt})
	domainWantStatus(t, err, 404)
	foreignBookings, err := f.s.ListBookings(f.ctx, foreignConsultant, f.start, f.start.Add(24*time.Hour), 100, 0)
	if err != nil || len(foreignBookings) != 0 {
		t.Fatalf("foreign consultant list: %+v %v", foreignBookings, err)
	}
	if _, err = f.s.SetOffering(f.ctx, f.admin, f.typ.ID, other.ID, false); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.s.ReplaceConsultationType(f.ctx, f.admin, f.typ.ID, ConsultationTypeInput{Title: "New visit", DurationMinutes: 90, Price: "200.01"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.s.GetBooking(f.ctx, f.client, b.ID)
	if err != nil || got.Price != "123.45" || got.DurationMinutes != 60 || got.TypeID != f.typ.ID {
		t.Fatalf("history changed: %+v %v", got, err)
	}
	var old ConsultationType
	if err = f.s.DB.GetContext(f.ctx, &old, `SELECT consultation_type_id,title,description,duration_minutes,price::text AS price,is_active FROM consultation_types WHERE consultation_type_id=$1`, f.typ.ID); err != nil {
		t.Fatal(err)
	}
	if old.Active || old.Price != "123.45" || old.DurationMinutes != 60 {
		t.Fatalf("old type mutated: %+v", old)
	}
	offerings, err := f.s.ListOfferings(f.ctx)
	if err != nil || len(offerings) != 1 || offerings[0].TypeID != replacement.ID || offerings[0].ConsultantID != f.consultantID {
		t.Fatalf("replacement assignments: %+v %v", offerings, err)
	}
	var activeOld int
	if err = f.s.DB.GetContext(f.ctx, &activeOld, `SELECT count(*) FROM consultation_offerings WHERE consultation_type_id=$1 AND is_active`, f.typ.ID); err != nil || activeOld != 0 {
		t.Fatalf("old offerings active=%d: %v", activeOld, err)
	}
	_, err = f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start.Add(2 * time.Hour)})
	domainWantStatus(t, err, 409)
	_, err = f.s.SetOffering(f.ctx, f.admin, f.typ.ID, f.consultantID, true)
	domainWantStatus(t, err, 409)
	newBooking, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: offerings[0].ID, StartsAt: f.start.Add(time.Hour)})
	if err != nil || newBooking.EndsAt.Sub(newBooking.StartsAt) != 90*time.Minute || newBooking.Price != "200.01" {
		t.Fatalf("new terms: %+v %v", newBooking, err)
	}
	if err = f.s.SetConsultantActive(f.ctx, f.admin, f.consultantID, false); err != nil {
		t.Fatal(err)
	}
	offerings, err = f.s.ListOfferings(f.ctx)
	if err != nil || len(offerings) != 0 {
		t.Fatalf("inactive consultant offerings: %+v %v", offerings, err)
	}
	consultants, err := f.s.ListConsultants(f.ctx)
	if err != nil || len(consultants) != 1 || consultants[0].ID != other.ID {
		t.Fatalf("public consultants: %+v %v", consultants, err)
	}
	types, err := f.s.ListConsultationTypes(f.ctx)
	if err != nil || len(types) != 1 || types[0].ID != replacement.ID {
		t.Fatalf("public types: %+v %v", types, err)
	}
}

func TestDomainStatusAtomicity(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	domainOneWinner(t, domainRace(func() error { _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "confirmed", nil); return err }, func() error {
		_, err := f.s.ChangeBookingStatus(f.ctx, f.otherAdmin, b.ID, "confirmed", nil)
		return err
	}))
	domainOneWinner(t, domainRace(func() error { _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "completed", nil); return err }, func() error { _, err := f.s.ChangeBookingStatus(f.ctx, f.otherAdmin, b.ID, "no_show", nil); return err }))
	events, err := f.s.ListBookingEvents(f.ctx, f.admin, b.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events: %+v %v", events, err)
	}
	for i, event := range events {
		if event.UserID == nil {
			t.Fatal("missing actor")
		}
		if i > 0 && event.OccurredAt.Before(events[i-1].OccurredAt) {
			t.Fatal("non-monotonic events")
		}
	}
	_, err = f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "cancelled", nil)
	domainWantStatus(t, err, 409)
}

func TestDomainScheduleProtectionAndSlots(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	_, err := f.s.SaveWorkInterval(f.ctx, f.consultant, f.consultantID, f.interval.ID, WorkIntervalInput{StartsAt: f.start.Add(30 * time.Minute), EndsAt: f.interval.EndsAt})
	domainWantStatus(t, err, 409)
	domainWantStatus(t, f.s.DeleteWorkInterval(f.ctx, f.admin, f.consultantID, f.interval.ID), 409)
	if _, err = f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "confirmed", nil); err != nil {
		t.Fatal(err)
	}
	domainWantStatus(t, f.s.DeleteWorkInterval(f.ctx, f.consultant, f.consultantID, f.interval.ID), 409)
	_, err = f.s.GetWorkInterval(f.ctx, f.client, f.consultantID, f.interval.ID)
	domainWantStatus(t, err, 403)
	w, err := f.s.GetWorkInterval(f.ctx, f.consultant, f.consultantID, f.interval.ID)
	if err != nil || !w.StartsAt.Equal(f.start) {
		t.Fatalf("failed edit persisted: %+v %v", w, err)
	}
	slots, err := f.s.AvailableSlots(f.ctx, f.offering.ID, f.start, f.start.Add(3*time.Hour))
	if err != nil || len(slots) != 5 || !slots[0].StartsAt.Equal(b.EndsAt) {
		t.Fatalf("slots: %+v %v", slots, err)
	}
	_, err = f.s.AvailableSlots(f.ctx, f.offering.ID, f.start, f.start.Add(32*24*time.Hour))
	domainWantStatus(t, err, 422)
	// Two adjoining intervals cannot collectively cover one booking.
	_, err = f.s.SaveWorkInterval(f.ctx, f.admin, f.consultantID, 0, WorkIntervalInput{StartsAt: f.start.Add(9 * time.Hour), EndsAt: f.start.Add(9*time.Hour + 30*time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SaveWorkInterval(f.ctx, f.admin, f.consultantID, 0, WorkIntervalInput{StartsAt: f.start.Add(9*time.Hour + 30*time.Minute), EndsAt: f.start.Add(10 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start.Add(9 * time.Hour)})
	domainWantStatus(t, err, 409)
	if _, err = f.s.ChangeBookingStatus(f.ctx, f.client, b.ID, "cancelled", nil); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteWorkInterval(f.ctx, f.admin, f.consultantID, f.interval.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDomainConcurrentScheduleBooking(t *testing.T) {
	f := newDomainFixture(t)
	errs := domainRace(func() error {
		_, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start})
		return err
	}, func() error { return f.s.DeleteWorkInterval(f.ctx, f.admin, f.consultantID, f.interval.ID) })
	domainOneWinner(t, errs)
}

func TestDomainConcurrentCatalogBooking(t *testing.T) {
	f := newDomainFixture(t)
	errs := domainRace(func() error {
		_, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start})
		return err
	}, func() error {
		_, err := f.s.ReplaceConsultationType(f.ctx, f.admin, f.typ.ID, ConsultationTypeInput{Title: "Replacement", DurationMinutes: 120, Price: "9.99"})
		return err
	})
	for _, err := range errs {
		if err != nil {
			domainWantStatus(t, err, 409)
		}
	}
	var active bool
	if err := f.s.DB.GetContext(f.ctx, &active, `SELECT is_active FROM consultation_types WHERE consultation_type_id=$1`, f.typ.ID); err != nil || active {
		t.Fatalf("replacement failed: active=%v err=%v", active, err)
	}
	list, err := f.s.ListBookings(f.ctx, f.admin, f.start, f.start.Add(24*time.Hour), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range list {
		if b.Price != "123.45" || b.DurationMinutes != 60 || b.EndsAt.Sub(b.StartsAt) != time.Hour {
			t.Fatalf("mixed catalog terms: %+v", b)
		}
	}
}

func TestDomainConcurrentAssignmentReplacement(t *testing.T) {
	f := newDomainFixture(t)
	var userID int64
	if err := f.s.DB.GetContext(f.ctx, &userID, `INSERT INTO users(name,email,password_hash,role,email_verified_at) VALUES('Unassigned','unassigned@test.invalid','dummy','client',clock_timestamp()) RETURNING user_id`); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.ProvisionConsultant(f.ctx, f.admin, userID, "")
	if err != nil {
		t.Fatal(err)
	}
	var assignmentErr error
	errs := domainRace(func() error {
		_, assignmentErr = f.s.SetOffering(f.ctx, f.otherAdmin, f.typ.ID, c.ID, true)
		return assignmentErr
	}, func() error {
		_, err := f.s.ReplaceConsultationType(f.ctx, f.admin, f.typ.ID, ConsultationTypeInput{Title: "Replacement", DurationMinutes: 60, Price: "10"})
		return err
	})
	for _, err := range errs {
		if err != nil {
			domainWantStatus(t, err, 409)
		}
	}
	var activeOld int
	if err := f.s.DB.GetContext(f.ctx, &activeOld, `SELECT count(*) FROM consultation_offerings WHERE consultation_type_id=$1 AND is_active`, f.typ.ID); err != nil || activeOld != 0 {
		t.Fatalf("active old assignments=%d err=%v", activeOld, err)
	}
	offerings, err := f.s.ListOfferings(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	if assignmentErr == nil {
		want = 2
	}
	if len(offerings) != want {
		t.Fatalf("replacement lost an assignment: got %+v, want %d assignments (assignment error %v)", offerings, want, assignmentErr)
	}
}

func TestDomainMonotonicEvents(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := f.s.DB.ExecContext(f.ctx, `INSERT INTO consultation_booking_status_events(consultation_booking_id,status,occurred_at,user_id) VALUES($1,'confirmed',$2,$3)`, b.ID, future, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "completed", nil); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.ListBookingEvents(f.ctx, f.admin, b.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events: %+v %v", events, err)
	}
	if events[2].Status != "completed" || events[2].OccurredAt.Before(future) || events[2].ID <= events[1].ID || events[2].UserID == nil || *events[2].UserID != f.admin.UserID {
		t.Fatalf("latest event did not preserve timestamp ordering and actor: %+v", events)
	}
	got, err := f.s.GetBooking(f.ctx, f.client, b.ID)
	if err != nil || got.Status != "completed" {
		t.Fatalf("current status: %+v %v", got, err)
	}
}

func TestDomainValidation(t *testing.T) {
	f := newDomainFixture(t)
	for _, price := range []string{"-1", "1e2", "1.001", "NaN", " 1", "10000000000", "1.", "+1", "01.00"} {
		t.Run(price, func(t *testing.T) {
			_, err := f.s.ReplaceConsultationType(f.ctx, f.admin, 0, ConsultationTypeInput{Title: "Bad", DurationMinutes: 60, Price: price})
			domainWantStatus(t, err, 422)
		})
	}
	_, err := f.s.ReplaceConsultationType(f.ctx, f.admin, 0, ConsultationTypeInput{Title: "Bad", DurationMinutes: 1441, Price: "1"})
	domainWantStatus(t, err, 422)
	_, err = f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: f.offering.ID, StartsAt: time.Now().Add(-time.Hour)})
	domainWantStatus(t, err, 422)
	_, err = f.s.GetBooking(f.ctx, f.client, -1)
	domainWantStatus(t, err, 422)
}

func TestDomainSessionValidation(t *testing.T) {
	for _, state := range []string{"missing", "unknown", "foreign", "revoked", "expired", "unverified"} {
		t.Run(state, func(t *testing.T) {
			f := newDomainFixture(t)
			b := f.book(t, f.client, f.start)
			actor := f.client
			var query string
			switch state {
			case "missing":
				actor.SessionID = 0
			case "unknown":
				actor.SessionID = 9223372036854775807
			case "foreign":
				actor.SessionID = f.otherClient.SessionID
			case "revoked":
				query = `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`
			case "expired":
				query = `UPDATE sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE user_id=$1`
			case "unverified":
				query = `UPDATE users SET email_verified_at=NULL WHERE user_id=$1`
			}
			if query != "" {
				if _, err := f.s.DB.ExecContext(f.ctx, query, actor.UserID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.s.GetProfile(f.ctx, actor)
			domainWantStatus(t, err, 401)
			_, err = f.s.ListAddresses(f.ctx, actor)
			domainWantStatus(t, err, 401)
			_, err = f.s.GetBooking(f.ctx, actor, b.ID)
			domainWantStatus(t, err, 401)
			_, err = f.s.ListBookings(f.ctx, actor, f.start, f.start.Add(24*time.Hour), 100, 0)
			domainWantStatus(t, err, 401)
			name := "Unauthorized edit"
			_, err = f.s.UpdateProfile(f.ctx, actor, ProfileUpdate{Name: &name})
			domainWantStatus(t, err, 401)
			_, err = f.s.CreateBooking(f.ctx, actor, BookingInput{OfferingID: f.offering.ID, StartsAt: f.start.Add(time.Hour)})
			domainWantStatus(t, err, 401)
			_, err = f.s.ChangeBookingStatus(f.ctx, actor, b.ID, "cancelled", nil)
			domainWantStatus(t, err, 401)
			_, err = f.s.ProvisionConsultant(f.ctx, actor, f.otherClient.UserID, "")
			domainWantStatus(t, err, 401)
			var gotName string
			if err = f.s.DB.GetContext(f.ctx, &gotName, `SELECT name FROM users WHERE user_id=$1`, actor.UserID); err != nil || gotName != "Test" {
				t.Fatalf("unauthorized profile edit persisted: name=%q err=%v", gotName, err)
			}
		})
	}
}

func TestDomainLogoutInvalidatesActor(t *testing.T) {
	f := newDomainFixture(t)
	actor := f.client
	if _, err := f.s.GetProfile(f.ctx, actor); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Logout(f.ctx, actor); err != nil {
		t.Fatal(err)
	}
	name := "Stale actor edit"
	_, err := f.s.UpdateProfile(f.ctx, actor, ProfileUpdate{Name: &name})
	domainWantStatus(t, err, 401)
	_, err = f.s.GetProfile(f.ctx, actor)
	domainWantStatus(t, err, 401)
}

func (f *domainFixture) waitBlocked(t *testing.T, blockerPID int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var blocked bool
		if err := f.s.DB.GetContext(f.ctx, &blocked, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)))`, blockerPID); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("domain operation did not wait for the user lock")
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
}

func TestDomainRevocationWhileWaitingForUserLock(t *testing.T) {
	f := newDomainFixture(t)
	tx, err := f.s.DB.BeginTxx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lockedID int64
	if err = tx.GetContext(f.ctx, &lockedID, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE`, f.client.UserID); err != nil {
		t.Fatal(err)
	}
	// Match reset/logout's lock order, but keep revocation uncommitted until the mutation is waiting.
	if _, err = tx.ExecContext(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, f.client.UserID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.GetContext(f.ctx, &pid, `SELECT pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		name := "Stale actor edit"
		_, err := f.s.UpdateProfile(f.ctx, f.client, ProfileUpdate{Name: &name})
		result <- err
	}()
	f.waitBlocked(t, pid)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	domainWantStatus(t, <-result, 401)
	var name string
	if err = f.s.DB.GetContext(f.ctx, &name, `SELECT name FROM users WHERE user_id=$1`, f.client.UserID); err != nil || name != "Test" {
		t.Fatalf("mutation survived revocation: name=%q err=%v", name, err)
	}
}

func TestDomainProvisionLocksUsersAscending(t *testing.T) {
	f := newDomainFixture(t)
	var actor Actor
	if err := f.s.DB.GetContext(f.ctx, &actor.UserID, `INSERT INTO users(name,email,password_hash,role,email_verified_at) VALUES('Admin','late-admin@test.invalid','dummy','admin',clock_timestamp()) RETURNING user_id`); err != nil {
		t.Fatal(err)
	}
	actor = f.session(t, actor)
	tx, err := f.s.DB.BeginTxx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int64
	if err = tx.GetContext(f.ctx, &id, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE`, f.client.UserID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.GetContext(f.ctx, &pid, `SELECT pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.s.ProvisionConsultant(f.ctx, actor, f.client.UserID, "")
		result <- err
	}()
	f.waitBlocked(t, pid)
	// Waiting for the smaller target must not hold the higher actor row.
	if err = tx.GetContext(f.ctx, &id, `SELECT user_id FROM users WHERE user_id=$1 FOR UPDATE NOWAIT`, actor.UserID); err != nil {
		t.Fatalf("provisioning acquired actor before lower target: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
