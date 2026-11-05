//go:build integration

package backend

import (
	"fmt"
	"testing"
)

func TestCatalogProfilesAndAddressOwnership(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	app := NewApp(s)
	owner, other := newHTTPBrowser(t, app), newHTTPBrowser(t, app)
	ownerID := owner.registerLogin(t, mailer, "owner@catalog.test")
	otherID := other.registerLogin(t, mailer, "other@catalog.test")
	var profile Profile
	owner.request(t, "PATCH", "/api/v1/me", map[string]string{"name": "Updated Owner", "phone": "12345"}, 200, &profile)
	if profile.UserID != ownerID || profile.Name != "Updated Owner" || profile.Phone == nil || *profile.Phone != "12345" || profile.ClientID == nil || profile.ConsultantID != nil {
		t.Fatalf("updated profile = %+v", profile)
	}
	owner.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	if profile.Name != "Updated Owner" || profile.Phone == nil || *profile.Phone != "12345" {
		t.Fatal("profile update was not persisted")
	}
	other.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	if profile.UserID != otherID || profile.Name == "Updated Owner" {
		t.Fatal("profile update crossed ownership boundary")
	}
	owner.request(t, "PATCH", "/api/v1/me", map[string]string{"bio": "not a consultant"}, 422, nil)
	owner.request(t, "PATCH", "/api/v1/me", map[string]string{"name": " "}, 422, nil)
	for _, field := range []string{"role", "user_id", "client_id", "email"} {
		t.Run("profile_rejects_"+field, func(t *testing.T) {
			owner.request(t, "PATCH", "/api/v1/me", map[string]any{field: "admin"}, 422, nil)
		})
	}
	var address Address
	owner.request(t, "POST", "/api/v1/addresses", map[string]string{"address_line": "First Street", "label": "Home"}, 201, &address)
	if address.ID <= 0 || address.AddressLine != "First Street" || address.Label == nil || *address.Label != "Home" {
		t.Fatalf("created address = %+v", address)
	}
	addressID, clientID := address.ID, address.ClientID
	path := fmt.Sprintf("/api/v1/addresses/%d", addressID)
	owner.request(t, "GET", path, nil, 200, &address)
	if address.ID != addressID || address.ClientID != clientID {
		t.Fatal("wrong address returned")
	}
	var addresses []Address
	owner.request(t, "GET", "/api/v1/addresses", nil, 200, &addresses)
	if len(addresses) != 1 || addresses[0].ID != addressID {
		t.Fatalf("owned addresses = %+v", addresses)
	}
	other.request(t, "GET", "/api/v1/addresses", nil, 200, &addresses)
	if len(addresses) != 0 {
		t.Fatal("foreign address leaked through list")
	}
	other.request(t, "GET", path, nil, 404, nil)
	other.request(t, "PUT", path, AddressInput{AddressLine: "Stolen"}, 404, nil)
	other.request(t, "DELETE", path, nil, 404, nil)
	owner.request(t, "GET", path, nil, 200, &address)
	if address.AddressLine != "First Street" {
		t.Fatal("foreign mutation changed address")
	}
	owner.request(t, "PUT", path, AddressInput{AddressLine: "Second Street"}, 200, &address)
	if address.ID != addressID || address.ClientID != clientID || address.AddressLine != "Second Street" || address.Label != nil {
		t.Fatalf("updated address = %+v", address)
	}
	owner.request(t, "POST", "/api/v1/addresses", AddressInput{}, 422, nil)
	owner.request(t, "POST", "/api/v1/addresses", map[string]any{"address_line": "Injected", "client_id": clientID}, 422, nil)
	owner.request(t, "DELETE", path, nil, 204, nil)
	owner.request(t, "GET", path, nil, 404, nil)
	owner.request(t, "PUT", path, AddressInput{AddressLine: "Gone"}, 404, nil)
	owner.request(t, "DELETE", path, nil, 404, nil)
	owner.request(t, "GET", "/api/v1/addresses", nil, 200, &addresses)
	if len(addresses) != 0 {
		t.Fatal("deleted address remained in list")
	}
	guest := newHTTPBrowser(t, app)
	guest.request(t, "GET", "/api/v1/me", nil, 401, nil)
	guest.request(t, "GET", "/api/v1/addresses", nil, 401, nil)
	guest.request(t, "POST", "/api/v1/addresses", AddressInput{AddressLine: "Guest"}, 401, nil)
}

func TestCatalogAdministrationAndImmutableReplacement(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	app := NewApp(s)
	admin, first, second := newHTTPBrowser(t, app), newHTTPBrowser(t, app), newHTTPBrowser(t, app)
	adminID := admin.registerLogin(t, mailer, "admin@catalog.test")
	firstID := first.registerLogin(t, mailer, "first@catalog.test")
	secondID := second.registerLogin(t, mailer, "second@catalog.test")
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE users SET role='admin' WHERE user_id=$1 AND email_verified_at IS NOT NULL`, adminID); err != nil {
		t.Fatal(err)
	}
	guest := newHTTPBrowser(t, app)
	for _, role := range []string{"admin", "consultant", "external"} {
		t.Run("registration_rejects_"+role, func(t *testing.T) {
			guest.request(t, "POST", "/api/v1/auth/register", map[string]string{
				"name": "Escalation", "email": role + "@escalation.test", "password": authTestPassword, "phone": "12345", "role": role,
			}, 422, nil)
		})
	}
	var escalations int
	if err := s.DB.GetContext(t.Context(), &escalations, `SELECT count(*) FROM users WHERE email LIKE '%@escalation.test'`); err != nil || escalations != 0 {
		t.Fatalf("role injection created %d users: %v", escalations, err)
	}
	provision := map[string]any{"user_id": firstID, "bio": "First consultant"}
	for _, path := range []string{"consultants", "consultation-types", "offerings", "consultant-candidates?email=first%40catalog.test"} {
		guest.request(t, "GET", "/api/v1/admin/"+path, nil, 401, nil)
		first.request(t, "GET", "/api/v1/admin/"+path, nil, 403, nil)
	}
	var candidate ConsultantCandidate
	admin.request(t, "GET", "/api/v1/admin/consultant-candidates?email=FIRST%40catalog.test", nil, 200, &candidate)
	if candidate.UserID != firstID || candidate.Name == "" {
		t.Fatalf("candidate = %+v", candidate)
	}
	guest.request(t, "POST", "/api/v1/admin/consultants", provision, 401, nil)
	first.request(t, "POST", "/api/v1/admin/consultants", provision, 403, nil)
	var c1, c2 Consultant
	admin.request(t, "POST", "/api/v1/admin/consultants", provision, 201, &c1)
	if c1.UserID != firstID || !c1.Active || c1.Bio == nil || *c1.Bio != "First consultant" {
		t.Fatalf("provisioned consultant = %+v", c1)
	}
	admin.request(t, "POST", "/api/v1/admin/consultants", provision, 404, nil)
	first.request(t, "POST", "/api/v1/admin/consultants", map[string]any{"user_id": secondID}, 403, nil)
	admin.request(t, "POST", "/api/v1/admin/consultants", map[string]any{"user_id": secondID}, 201, &c2)
	var profile Profile
	first.request(t, "PATCH", "/api/v1/me", map[string]string{"bio": "Updated bio", "phone": "54321"}, 200, &profile)
	if profile.Role != "consultant" || profile.ClientID == nil || profile.ConsultantID == nil || *profile.ConsultantID != c1.ID || profile.Bio == nil || *profile.Bio != "Updated bio" || profile.Phone == nil || *profile.Phone != "54321" {
		t.Fatalf("consultant profile did not preserve client: %+v", profile)
	}
	typeInput := ConsultationTypeInput{Title: "Original", Description: "Original terms", DurationMinutes: 30, Price: "100.00"}
	first.request(t, "POST", "/api/v1/admin/consultation-types", typeInput, 403, nil)
	var old, replacement ConsultationType
	admin.request(t, "POST", "/api/v1/admin/consultation-types", typeInput, 201, &old)
	offeringInput := map[string]any{"consultation_type_id": old.ID, "consultant_id": c1.ID, "is_active": true}
	first.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 403, nil)
	var originalOffering, inactiveOffering Offering
	admin.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 200, &originalOffering)
	offeringInput["consultant_id"], offeringInput["is_active"] = c2.ID, false
	admin.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 200, &inactiveOffering)
	typePath := fmt.Sprintf("/api/v1/admin/consultation-types/%d", old.ID)
	newInput := ConsultationTypeInput{Title: "Replacement", Description: "New terms", DurationMinutes: 60, Price: "150.50"}
	first.request(t, "PUT", typePath, newInput, 403, nil)
	admin.request(t, "PUT", typePath, newInput, 201, &replacement)
	if replacement.ID == old.ID || !replacement.Active || replacement.Title != newInput.Title || replacement.Description != newInput.Description || replacement.DurationMinutes != 60 || replacement.Price != "150.50" {
		t.Fatalf("replacement = %+v", replacement)
	}
	var preserved ConsultationType
	if err := s.DB.GetContext(t.Context(), &preserved, `SELECT consultation_type_id,title,description,duration_minutes,price::text AS price,is_active FROM consultation_types WHERE consultation_type_id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	old.Active = false
	if preserved != old {
		t.Fatalf("original terms changed: %+v, want %+v", preserved, old)
	}
	var historical []Offering
	if err := s.DB.SelectContext(t.Context(), &historical, `SELECT consultation_offering_id,consultant_id,consultation_type_id,is_active FROM consultation_offerings WHERE consultation_type_id=$1 ORDER BY consultation_offering_id`, old.ID); err != nil {
		t.Fatal(err)
	}
	originalOffering.Active = false
	if len(historical) != 2 || historical[0] != originalOffering || historical[1] != inactiveOffering {
		t.Fatalf("historical offerings not retained inactive: %+v", historical)
	}
	var assigned []Offering
	if err := s.DB.SelectContext(t.Context(), &assigned, `SELECT consultation_offering_id,consultant_id,consultation_type_id,is_active FROM consultation_offerings WHERE consultation_type_id=$1`, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 1 || assigned[0].ConsultantID != c1.ID || !assigned[0].Active || assigned[0].ID == originalOffering.ID {
		t.Fatalf("replacement must copy only active assignments: %+v", assigned)
	}
	admin.request(t, "PUT", typePath, newInput, 409, nil)
	offeringInput["consultant_id"], offeringInput["is_active"] = c1.ID, true
	admin.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 409, nil)
	var types []ConsultationType
	guest.request(t, "GET", "/api/v1/consultation-types", nil, 200, &types)
	if len(types) != 1 || types[0] != replacement {
		t.Fatalf("public types = %+v", types)
	}
	var offerings []Offering
	guest.request(t, "GET", "/api/v1/offerings", nil, 200, &offerings)
	if len(offerings) != 1 || offerings[0] != assigned[0] {
		t.Fatalf("public offerings = %+v", offerings)
	}
	consultantPath := fmt.Sprintf("/api/v1/admin/consultants/%d", c1.ID)
	first.request(t, "PATCH", consultantPath, map[string]bool{"is_active": false}, 403, nil)
	admin.request(t, "PATCH", consultantPath, map[string]bool{}, 422, nil)
	admin.request(t, "PATCH", consultantPath, map[string]bool{"is_active": false}, 204, nil)
	var consultants []Consultant
	guest.request(t, "GET", "/api/v1/consultants", nil, 200, &consultants)
	if len(consultants) != 1 || consultants[0].ID != c2.ID {
		t.Fatalf("inactive consultant leaked: %+v", consultants)
	}
	guest.request(t, "GET", "/api/v1/offerings", nil, 200, &offerings)
	if len(offerings) != 0 {
		t.Fatal("inactive consultant's offering leaked")
	}
	admin.request(t, "PATCH", consultantPath, map[string]bool{"is_active": true}, 204, nil)
	guest.request(t, "GET", "/api/v1/offerings", nil, 200, &offerings)
	if len(offerings) != 1 || offerings[0] != assigned[0] {
		t.Fatal("reactivated consultant's offering missing")
	}
	offeringInput["consultation_type_id"], offeringInput["is_active"] = replacement.ID, false
	admin.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 200, nil)
	guest.request(t, "GET", "/api/v1/offerings", nil, 200, &offerings)
	if len(offerings) != 0 {
		t.Fatal("inactive offering leaked")
	}
	admin.request(t, "GET", "/api/v1/admin/consultation-types?limit=1&offset=0", nil, 200, &types)
	if len(types) != 1 || types[0] != old {
		t.Fatalf("admin history page = %+v", types)
	}
	first.request(t, "DELETE", fmt.Sprintf("/api/v1/admin/consultation-types/%d", replacement.ID), nil, 403, nil)
	offeringInput["is_active"] = true
	admin.request(t, "PUT", "/api/v1/admin/offerings", offeringInput, 200, nil)
	for range 2 {
		admin.request(t, "DELETE", fmt.Sprintf("/api/v1/admin/consultation-types/%d", replacement.ID), nil, 204, nil)
	}
	admin.request(t, "GET", "/api/v1/admin/offerings", nil, 200, &offerings)
	if len(offerings) != 3 || offerings[2].Active || offerings[2].TypeID != replacement.ID {
		t.Fatalf("admin offerings after deactivation = %+v", offerings)
	}
	guest.request(t, "GET", "/api/v1/consultation-types", nil, 200, &types)
	if len(types) != 0 {
		t.Fatalf("deactivated type in public list: %+v", types)
	}
	first.request(t, "PUT", consultantPath+"/bio", map[string]string{"bio": "Denied"}, 403, nil)
	admin.request(t, "PUT", consultantPath+"/bio", map[string]string{"bio": "Updated by admin"}, 204, nil)
	admin.request(t, "PATCH", consultantPath, map[string]bool{"is_active": false}, 204, nil)
	admin.request(t, "GET", "/api/v1/admin/consultants", nil, 200, &consultants)
	if len(consultants) != 2 || consultants[0].Active || consultants[0].Bio == nil || *consultants[0].Bio != "Updated by admin" {
		t.Fatalf("admin consultants = %+v", consultants)
	}
	var own []AssignedOffering
	first.request(t, "GET", "/api/v1/me/offerings", nil, 200, &own)
	if len(own) != 2 || own[0].ConsultantID != c1.ID || own[1].Title != replacement.Title || own[1].Available {
		t.Fatalf("own offerings = %+v", own)
	}
}
