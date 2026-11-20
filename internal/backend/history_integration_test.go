//go:build integration

package backend

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shinyzero00/barone-sanitation/internal/migrations"
)

func historyWantSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got error %v, want SQLSTATE %s", err, code)
	}
}

func TestHistoryImmutableGuards(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	events, err := f.s.ListBookingEvents(f.ctx, f.admin, b.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("initial events: %+v, %v", events, err)
	}
	unused, err := f.s.ReplaceConsultationType(f.ctx, f.admin, 0, ConsultationTypeInput{Title: "Unused", DurationMinutes: 30, Price: "10"})
	if err != nil {
		t.Fatal(err)
	}
	otherConsultant, err := f.s.ProvisionConsultant(f.ctx, f.admin, f.otherClient.UserID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherOffering, err := f.s.SetOffering(f.ctx, f.admin, unused.ID, otherConsultant.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	address, err := f.s.SaveAddress(f.ctx, f.client, 0, AddressInput{AddressLine: "123 Main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		query string
		args  []any
	}{
		{"event_update", `UPDATE consultation_booking_status_events SET reason='Rewritten' WHERE consultation_booking_status_event_id=$1`, []any{events[0].ID}},
		{"event_delete", `DELETE FROM consultation_booking_status_events WHERE consultation_booking_status_event_id=$1`, []any{events[0].ID}},
		{"event_truncate", `TRUNCATE consultation_booking_status_events`, nil},
		{"type_price", `UPDATE consultation_types SET price=price+1 WHERE consultation_type_id=$1`, []any{f.typ.ID}},
		{"type_duration", `UPDATE consultation_types SET duration_minutes=duration_minutes+1 WHERE consultation_type_id=$1`, []any{f.typ.ID}},
		{"type_title", `UPDATE consultation_types SET title='Rewritten' WHERE consultation_type_id=$1`, []any{f.typ.ID}},
		{"type_description", `UPDATE consultation_types SET description='Rewritten' WHERE consultation_type_id=$1`, []any{f.typ.ID}},
		{"offering_consultant", `UPDATE consultation_offerings SET consultant_id=$2 WHERE consultation_offering_id=$1`, []any{f.offering.ID, otherConsultant.ID}},
		{"offering_type", `UPDATE consultation_offerings SET consultation_type_id=$2 WHERE consultation_offering_id=$1`, []any{f.offering.ID, unused.ID}},
		{"type_delete", `DELETE FROM consultation_types WHERE consultation_type_id=$1`, []any{f.typ.ID}},
		{"unused_type_delete", `DELETE FROM consultation_types WHERE consultation_type_id=$1`, []any{unused.ID}},
		{"offering_delete", `DELETE FROM consultation_offerings WHERE consultation_offering_id=$1`, []any{f.offering.ID}},
		{"unused_offering_delete", `DELETE FROM consultation_offerings WHERE consultation_offering_id=$1`, []any{otherOffering.ID}},
		{"booking_delete", `DELETE FROM consultation_bookings WHERE consultation_booking_id=$1`, []any{b.ID}},
		{"type_truncate", `TRUNCATE consultation_types CASCADE`, nil},
		{"offering_truncate", `TRUNCATE consultation_offerings CASCADE`, nil},
		{"booking_truncate", `TRUNCATE consultation_bookings CASCADE`, nil},
		{"booking_start", `UPDATE consultation_bookings SET starts_at=starts_at+interval '1 minute' WHERE consultation_booking_id=$1`, []any{b.ID}},
		{"booking_end", `UPDATE consultation_bookings SET ends_at=ends_at+interval '1 minute' WHERE consultation_booking_id=$1`, []any{b.ID}},
		{"booking_address", `UPDATE consultation_bookings SET client_address_id=$2 WHERE consultation_booking_id=$1`, []any{b.ID, address.ID}},
		{"booking_offering", `UPDATE consultation_bookings SET consultation_offering_id=$2 WHERE consultation_booking_id=$1`, []any{b.ID, otherOffering.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := f.s.DB.BeginTxx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = tx.ExecContext(f.ctx, tt.query, tt.args...)
			historyWantSQLState(t, err, "23514")
		})
	}
	// No offering references this type: its deletion must not merely fail on a foreign key.
	unreferenced, err := f.s.ReplaceConsultationType(f.ctx, f.admin, 0, ConsultationTypeInput{Title: "Unreferenced", DurationMinutes: 30, Price: "1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DB.ExecContext(f.ctx, `DELETE FROM consultation_types WHERE consultation_type_id=$1`, unreferenced.ID)
	historyWantSQLState(t, err, "23514")
}

func TestHistoryActiveChangesAndReplacement(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	for _, active := range []bool{false, true} {
		offering, err := f.s.SetOffering(f.ctx, f.admin, f.typ.ID, f.consultantID, active)
		if err != nil || offering.ID != f.offering.ID || offering.Active != active {
			t.Fatalf("offering active=%v: %+v, %v", active, offering, err)
		}
		if err := f.s.SetConsultantActive(f.ctx, f.admin, f.consultantID, active); err != nil {
			t.Fatal(err)
		}
	}
	replacement, err := f.s.ReplaceConsultationType(f.ctx, f.admin, f.typ.ID, ConsultationTypeInput{Title: "Replacement", Description: "New terms", DurationMinutes: 90, Price: "200"})
	if err != nil || replacement.ID == f.typ.ID {
		t.Fatalf("replacement: %+v, %v", replacement, err)
	}
	_, err = f.s.DB.ExecContext(f.ctx, `UPDATE consultation_types SET is_active=true WHERE consultation_type_id=$1`, f.typ.ID)
	historyWantSQLState(t, err, "23514")
	var active bool
	if err = f.s.DB.GetContext(f.ctx, &active, `SELECT is_active FROM consultation_offerings WHERE consultation_offering_id=$1`, f.offering.ID); err != nil || active {
		t.Fatalf("old offering active=%v: %v", active, err)
	}
	got, err := f.s.GetBooking(f.ctx, f.client, b.ID)
	if err != nil || got.TypeID != f.typ.ID || got.ConsultationTitle != f.typ.Title || got.Price != "123.45" || got.DurationMinutes != 60 || !got.StartsAt.Equal(b.StartsAt) || !got.EndsAt.Equal(b.EndsAt) {
		t.Fatalf("historical booking: %+v, %v", got, err)
	}
	offerings, err := f.s.ListOfferings(f.ctx)
	if err != nil || len(offerings) != 1 || offerings[0].TypeID != replacement.ID || offerings[0].ConsultantID != f.consultantID {
		t.Fatalf("replacement offerings: %+v, %v", offerings, err)
	}
	newBooking, err := f.s.CreateBooking(f.ctx, f.client, BookingInput{OfferingID: offerings[0].ID, StartsAt: f.start.Add(time.Hour)})
	if err != nil || newBooking.Price != "200.00" || newBooking.EndsAt.Sub(newBooking.StartsAt) != 90*time.Minute {
		t.Fatalf("replacement booking: %+v, %v", newBooking, err)
	}
	if _, err = f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "confirmed", nil); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.ListBookingEvents(f.ctx, f.admin, b.ID)
	if err != nil || len(events) != 2 || events[0].Status != "pending" || events[1].Status != "confirmed" {
		t.Fatalf("appended events: %+v, %v", events, err)
	}
}

func TestHistoryDatabaseConstraints(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	foreign, err := f.s.SaveAddress(f.ctx, f.otherClient, 0, AddressInput{AddressLine: "Foreign address"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DB.ExecContext(f.ctx, `INSERT INTO consultation_bookings(client_id,consultation_offering_id,client_address_id,starts_at,ends_at) VALUES($1,$2,$3,$4,$5)`, b.ClientID, f.offering.ID, foreign.ID, b.StartsAt, b.EndsAt)
	historyWantSQLState(t, err, "23503")
	for _, tt := range []struct {
		name  string
		query string
		args  []any
	}{
		{"duration", `INSERT INTO consultation_types(title,description,duration_minutes,price) VALUES('Invalid','',0,1)`, nil},
		{"price", `INSERT INTO consultation_types(title,description,duration_minutes,price) VALUES('Invalid','',30,-1)`, nil},
		{"booking_times", `INSERT INTO consultation_bookings(client_id,consultation_offering_id,starts_at,ends_at) VALUES($1,$2,$3,$3)`, []any{b.ClientID, f.offering.ID, b.StartsAt}},
		{"event_status", `INSERT INTO consultation_booking_status_events(consultation_booking_id,status,occurred_at) VALUES($1,'invalid',clock_timestamp())`, []any{b.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.s.DB.ExecContext(f.ctx, tt.query, tt.args...)
			historyWantSQLState(t, err, "23514")
		})
	}
}

func TestHistoryMigrationRoundtrip(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	version := func(want int64) {
		t.Helper()
		var got int64
		if err := f.s.DB.GetContext(f.ctx, &got, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`); err != nil || got != want {
			t.Fatalf("migration version=%d, want %d: %v", got, want, err)
		}
	}
	// Capture schema-local objects, including indexes, identities, and guard definitions.
	objects := func() []string {
		t.Helper()
		var got []string
		if err := f.s.DB.SelectContext(f.ctx, &got, `
			SELECT 'relation:' || relkind::text || ':' || relname AS object FROM pg_class WHERE relnamespace=current_schema()::regnamespace
			UNION ALL SELECT 'function:' || proname || ':' || pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace=current_schema()::regnamespace
			UNION ALL SELECT 'trigger:' || pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE c.relnamespace=current_schema()::regnamespace AND NOT t.tgisinternal
			ORDER BY object`); err != nil {
			t.Fatal(err)
		}
		return got
	}
	version(8)
	wantObjects := objects()
	if err := migrations.Run(f.ctx, f.s.DB.DB, true); err != nil {
		t.Fatal(err)
	}
	version(7)
	if err := migrations.Run(f.ctx, f.s.DB.DB, true); err != nil {
		t.Fatal(err)
	}
	version(6)
	if err := migrations.Run(f.ctx, f.s.DB.DB, true); err != nil {
		t.Fatal(err)
	}
	version(5)
	result, err := f.s.DB.ExecContext(f.ctx, `UPDATE consultation_types SET price=price WHERE consultation_type_id=$1`, f.typ.ID)
	if err != nil {
		t.Fatalf("history guard still installed after down: %v", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("preserved types=%d: %v", n, err)
	}
	var count int
	if err = f.s.DB.GetContext(f.ctx, &count, `SELECT count(*) FROM consultation_bookings b JOIN consultation_booking_status_events e USING(consultation_booking_id) WHERE b.consultation_booking_id=$1`, b.ID); err != nil || count != 1 {
		t.Fatalf("preserved booking/events=%d: %v", count, err)
	}
	var guards int
	if err = f.s.DB.GetContext(f.ctx, &guards, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE c.relnamespace=current_schema()::regnamespace AND NOT t.tgisinternal`); err != nil || guards != 0 {
		t.Fatalf("guards remaining=%d: %v", guards, err)
	}
	// This fixture's schema is isolated; intentionally roll back all remaining stages.
	for want := int64(4); want >= 0; want-- {
		if err = migrations.Run(f.ctx, f.s.DB.DB, true); err != nil {
			t.Fatal(err)
		}
		version(want)
	}
	version(0)
	var absent bool
	if err = f.s.DB.GetContext(f.ctx, &absent, `SELECT to_regclass('users') IS NULL`); err != nil || !absent {
		t.Fatalf("users not dropped: absent=%v, %v", absent, err)
	}
	for range 2 {
		if err = migrations.Run(f.ctx, f.s.DB.DB, false); err != nil {
			t.Fatal(err)
		}
		version(8)
		if got := objects(); !reflect.DeepEqual(got, wantObjects) {
			t.Fatalf("schema objects differ after up: got %v, want %v", got, wantObjects)
		}
	}
	if err = f.s.DB.GetContext(f.ctx, &count, `SELECT count(*) FROM users`); err != nil || count != 0 {
		t.Fatalf("fixture users survived initial down: count=%d, %v", count, err)
	}
	var typeID int64
	if err = f.s.DB.GetContext(f.ctx, &typeID, `INSERT INTO consultation_types(title,description,duration_minutes,price) VALUES('Restored','',60,1) RETURNING consultation_type_id`); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DB.ExecContext(f.ctx, `UPDATE consultation_types SET price=2 WHERE consultation_type_id=$1`, typeID)
	historyWantSQLState(t, err, "23514")
	_, err = f.s.DB.ExecContext(f.ctx, `TRUNCATE consultation_booking_status_events`)
	historyWantSQLState(t, err, "23514")
}

func TestHistoryConcurrentMigration(t *testing.T) {
	f := newDomainFixture(t)
	objects := func() []string {
		t.Helper()
		var got []string
		if err := f.s.DB.SelectContext(f.ctx, &got, `
			SELECT 'relation:' || relkind::text || ':' || relname AS object FROM pg_class WHERE relnamespace=current_schema()::regnamespace
			UNION ALL SELECT 'function:' || proname || ':' || pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace=current_schema()::regnamespace
			UNION ALL SELECT 'trigger:' || pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE c.relnamespace=current_schema()::regnamespace AND NOT t.tgisinternal
			ORDER BY object`); err != nil {
			t.Fatal(err)
		}
		return got
	}
	wantObjects := objects()
	// Only this fixture's isolated schema is rolled back, including its seeded data.
	for range 8 {
		if err := migrations.Run(f.ctx, f.s.DB.DB, true); err != nil {
			t.Fatal(err)
		}
	}
	var version int64
	const versionQuery = `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`
	if err := f.s.DB.GetContext(f.ctx, &version, versionQuery); err != nil || version != 0 {
		t.Fatalf("migration version=%d, want 0: %v", version, err)
	}
	up := func() error { return migrations.Run(f.ctx, f.s.DB.DB, false) }
	for i, err := range domainRace(up, up) {
		if err != nil {
			t.Errorf("concurrent migration result %d: %v", i, err)
		}
	}
	if err := f.s.DB.GetContext(f.ctx, &version, versionQuery); err != nil || version != 8 {
		t.Fatalf("migration version=%d, want 8: %v", version, err)
	}
	if got := objects(); !reflect.DeepEqual(got, wantObjects) {
		t.Fatalf("schema objects differ after concurrent up: got %v, want %v", got, wantObjects)
	}
}
