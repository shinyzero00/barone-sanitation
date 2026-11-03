//go:build integration

package backend

import (
	"context"
	"sync"
	"testing"
	"time"
)

func completedBooking(t *testing.T, f *domainFixture) Booking {
	t.Helper()
	b := f.book(t, f.client, f.start)
	if _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "confirmed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "completed", nil); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConsultationResultLifecycleAndAccess(t *testing.T) {
	f := newDomainFixture(t)
	b := f.book(t, f.client, f.start)
	_, err := f.s.CreateConsultationResult(f.ctx, f.consultant, b.ID, ConsultationResultInput{Result: "before completion"})
	domainWantStatus(t, err, 409)
	if _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "confirmed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeBookingStatus(f.ctx, f.admin, b.ID, "completed", nil); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.CreateConsultationResult(f.ctx, f.consultant, b.ID, ConsultationResultInput{Result: "  completed notes  "})
	if err != nil || result.Result != "completed notes" {
		t.Fatalf("create result: %+v %v", result, err)
	}
	for _, actor := range []Actor{f.client, f.consultant, f.admin} {
		got, err := f.s.GetConsultationResult(f.ctx, actor, b.ID)
		if err != nil || got.Result != "completed notes" {
			t.Fatalf("read result as %+v: %+v %v", actor, got, err)
		}
	}
	if _, err = f.s.GetConsultationResult(f.ctx, f.otherClient, b.ID); err == nil {
		t.Fatal("foreign client read result")
	} else {
		domainWantStatus(t, err, 404)
	}
	_, err = f.s.CreateConsultationResult(f.ctx, f.consultant, b.ID, ConsultationResultInput{Result: "replacement"})
	domainWantStatus(t, err, 409)
	got, err := f.s.GetConsultationResult(f.ctx, f.client, b.ID)
	if err != nil || got.Result != "completed notes" {
		t.Fatalf("result was overwritten: %+v %v", got, err)
	}
	if _, err = f.s.CreateConsultationResult(f.ctx, f.consultant, b.ID, ConsultationResultInput{Result: " \t\n "}); err == nil {
		t.Fatal("blank result accepted")
	}
}

func TestConsultationResultMissingAndConcurrentCreate(t *testing.T) {
	f := newDomainFixture(t)
	b := completedBooking(t, f)
	if _, err := f.s.GetConsultationResult(f.ctx, f.client, b.ID); err == nil {
		t.Fatal("missing result accepted")
	} else {
		domainWantStatus(t, err, 404)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			<-start
			_, err := f.s.CreateConsultationResult(f.ctx, f.consultant, b.ID, ConsultationResultInput{Result: text})
			errs <- err
		}(text)
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
		} else if api, ok := err.(*APIError); ok && api.Status == 409 {
			conflicts++
		} else {
			t.Fatalf("concurrent result error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}

	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	got, err := f.s.GetConsultationResult(ctx, f.admin, b.ID)
	if err != nil || (got.Result != "first" && got.Result != "second") {
		t.Fatalf("stored concurrent result: %+v %v", got, err)
	}
}
