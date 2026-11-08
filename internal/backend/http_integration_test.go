//go:build integration

package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

type httpTestBrowser struct {
	app     *fiber.App
	csrf    *http.Cookie
	cookies map[string]*http.Cookie
}

func newHTTPBrowser(t *testing.T, app *fiber.App) *httpTestBrowser {
	t.Helper()
	return &httpTestBrowser{app: app, csrf: httpCSRFTestCookie(t, app), cookies: make(map[string]*http.Cookie)}
}

func (b *httpTestBrowser) request(t *testing.T, method, path string, in any, status int, out any) *http.Response {
	t.Helper()
	body := ""
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	return b.requestRaw(t, method, path, body, status, out)
}

func (b *httpTestBrowser) requestRaw(t *testing.T, method, path, body string, status int, out any) *http.Response {
	t.Helper()
	request := httpUnsafeRequest(method, path, body, b.csrf)
	for _, cookie := range b.cookies {
		request.AddCookie(cookie)
	}
	response := httpDo(t, b.app, request, status)
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	} else if status == 204 {
		content, err := io.ReadAll(response.Body)
		if err != nil || len(content) != 0 {
			t.Fatal("authentication response must have no body")
		}
	}
	return response
}

func (b *httpTestBrowser) registerLogin(t *testing.T, mailer *authRecordingMailer, email string) int64 {
	t.Helper()
	var registered map[string]int64
	b.request(t, "POST", "/api/v1/auth/register", RegisterInput{Name: "HTTP Client", Email: email, Password: authTestPassword, Phone: "+12025550100"}, 201, &registered)
	if len(registered) != 1 || registered["user_id"] <= 0 {
		t.Fatal("registration exposed unexpected fields")
	}
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": authTestPassword}, 401, nil)
	b.request(t, "POST", "/api/v1/auth/verify-email", CodeInput{Email: email, Code: mailer.Code(email)}, 204, nil)
	b.login(t, mailer, email, authTestPassword)
	return registered["user_id"]
}

func (b *httpTestBrowser) login(t *testing.T, mailer *authRecordingMailer, email, password string) {
	t.Helper()
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, 204, nil)
	challenge := b.cookies[challengeCookie]
	if challenge == nil || !challenge.Secure || !challenge.HttpOnly || challenge.SameSite != http.SameSiteStrictMode || challenge.Path != "/" || challenge.Domain != "" || challenge.MaxAge != 600 {
		t.Fatal("login challenge cookie contract violated")
	}
	// A valid OTP without the browser challenge is not sufficient.
	delete(b.cookies, challengeCookie)
	b.request(t, "POST", "/api/v1/auth/login/verify", CodeInput{Email: email, Code: mailer.Code(email)}, 401, nil)
	b.cookies[challengeCookie] = challenge
	b.request(t, "POST", "/api/v1/auth/login/verify", CodeInput{Email: email, Code: mailer.Code(email)}, 204, nil)
	if b.cookies[challengeCookie] != nil {
		t.Fatal("challenge was not cleared")
	}
	session := b.cookies[sessionCookie]
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" || session.Domain != "" || session.MaxAge != 3600 {
		t.Fatal("session cookie contract violated")
	}
}

func TestHTTPAuthEndToEnd(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	app := NewApp(s)
	b := newHTTPBrowser(t, app)
	email := "http@example.com"
	id := b.registerLogin(t, mailer, email)
	var profile Profile
	b.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	if profile.UserID != id || profile.Role != "client" || profile.Email != email {
		t.Fatal("wrong authenticated profile")
	}
	var unchanged Profile
	b.request(t, "PATCH", "/api/v1/me", map[string]any{"name": nil, "phone": nil, "bio": nil}, 200, &unchanged)
	if unchanged.Email != email || unchanged.Name != profile.Name || unchanged.Phone == nil || profile.Phone == nil || *unchanged.Phone != *profile.Phone || unchanged.Bio != nil {
		t.Fatal("null PATCH fields changed profile")
	}
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"null", "null", 400, "invalid_json"},
		{"array", "[]", 400, "invalid_json"},
		{"unknown", `{"password":"logout-secret"}`, 422, "invalid_request"},
	} {
		t.Run("logout "+tc.name, func(t *testing.T) {
			response := b.requestRaw(t, "POST", "/api/v1/auth/logout", tc.body, tc.status, nil)
			httpAssertSafeError(t, response, tc.code, "logout-secret")
			b.request(t, "GET", "/api/v1/me", nil, 200, nil)
		})
	}
	session := b.cookies[sessionCookie]
	b.request(t, "POST", "/api/v1/auth/logout", struct{}{}, 204, nil)
	b.cookies[sessionCookie] = session
	b.request(t, "GET", "/api/v1/me", nil, 401, nil)
	delete(b.cookies, sessionCookie)
	authAllowResend(t, s, email, "login")
	b.login(t, mailer, email, authTestPassword)
	b.request(t, "POST", "/api/v1/auth/codes", map[string]string{"email": email, "purpose": "reset_password"}, 204, nil)
	b.request(t, "POST", "/api/v1/auth/reset-password", map[string]string{"email": email, "code": mailer.Code(email), "password": "replacement secure password"}, 204, nil)
	b.request(t, "GET", "/api/v1/me", nil, 401, nil)
	b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": authTestPassword}, 401, nil)
	authAllowResend(t, s, email, "login")
	b.login(t, mailer, email, "replacement secure password")
	b.request(t, "GET", "/api/v1/me", nil, 200, &profile)
	httpDo(t, app, httptest.NewRequest("GET", "/healthz", nil), 200)
	// Public APIs share the global budget, and verification has a separate common budget.
	var buckets []string
	if err := s.DB.SelectContext(t.Context(), &buckets, `SELECT DISTINCT bucket FROM ip_rate_limits ORDER BY bucket`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(buckets, ","), "http") || !strings.Contains(strings.Join(buckets, ","), "verify") {
		t.Fatal("missing HTTP rate budgets")
	}
	b.request(t, "POST", "/api/v1/auth/logout", nil, 204, nil)
	b.request(t, "GET", "/api/v1/me", nil, 401, nil)
}

func TestHTTPCodeTiming(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	b := newHTTPBrowser(t, NewApp(s))
	const email = "timing@example.com"
	check := func(response *http.Response, purpose string) {
		t.Helper()
		expiresAt, expiryErr := time.Parse(time.RFC3339Nano, response.Header.Get("OTP-Expires-At"))
		resendAt, resendErr := time.Parse(time.RFC3339Nano, response.Header.Get("OTP-Resend-At"))
		if expiryErr != nil || resendErr != nil || expiresAt.Sub(resendAt) != s.Config.CodeTTL-s.Config.ResendInterval {
			t.Fatalf("invalid OTP timing headers: %v, %v", expiryErr, resendErr)
		}
		if purpose == "" {
			return
		}
		var stored struct {
			ExpiresAt time.Time `db:"expires_at"`
			ResendAt  time.Time `db:"regenerate_available_at"`
		}
		if err := s.DB.GetContext(t.Context(), &stored, `SELECT expires_at,regenerate_available_at FROM verification_codes WHERE email=$1 AND purpose=$2`, email, purpose); err != nil {
			t.Fatal(err)
		}
		if !expiresAt.Equal(stored.ExpiresAt) || !resendAt.Equal(stored.ResendAt) {
			t.Fatal("response timing differs from persisted timing")
		}
	}
	check(b.request(t, "POST", "/api/v1/auth/register", RegisterInput{Name: "Timing", Email: email, Password: authTestPassword, Phone: "12345678"}, 201, nil), "verify_email")
	b.request(t, "POST", "/api/v1/auth/verify-email", CodeInput{Email: email, Code: mailer.Code(email)}, 204, nil)
	check(b.request(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": authTestPassword}, 204, nil), "login")
	check(b.request(t, "POST", "/api/v1/auth/codes", map[string]string{"email": email, "purpose": "reset_password"}, 204, nil), "reset_password")
	check(b.request(t, "POST", "/api/v1/auth/codes", map[string]string{"email": "missing@example.com", "purpose": "reset_password"}, 204, nil), "")
}

func TestHTTPOwnershipSchedulingAndStatus(t *testing.T) {
	s, mailer := authFixture(t)
	s.Config.PublicOrigin, s.Config.SlotStep = httpTestOrigin, 15*time.Minute
	app := NewApp(s)
	client, other, admin, consultant := newHTTPBrowser(t, app), newHTTPBrowser(t, app), newHTTPBrowser(t, app), newHTTPBrowser(t, app)
	client.registerLogin(t, mailer, "client@http.test")
	other.registerLogin(t, mailer, "other@http.test")
	adminID := admin.registerLogin(t, mailer, "admin@http.test")
	consultantID := consultant.registerLogin(t, mailer, "consultant@http.test")
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE users SET role='admin' WHERE user_id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	var c Consultant
	admin.request(t, "POST", "/api/v1/admin/consultants", map[string]any{"user_id": consultantID, "bio": "A consultant"}, 201, &c)
	activationPath := fmt.Sprintf("/api/v1/admin/consultants/%d", c.ID)
	for _, body := range []string{`{}`, `{"is_active":null}`} {
		response := admin.requestRaw(t, "PATCH", activationPath, body, 422, nil)
		httpAssertSafeError(t, response, "invalid_request")
	}
	admin.request(t, "PATCH", activationPath, map[string]bool{"is_active": false}, 204, nil)
	var active bool
	if err := s.DB.GetContext(t.Context(), &active, `SELECT is_active FROM consultants WHERE consultant_id=$1`, c.ID); err != nil || active {
		t.Fatalf("explicit false was not persisted: active=%v err=%v", active, err)
	}
	admin.request(t, "PATCH", activationPath, map[string]bool{"is_active": true}, 204, nil)
	var typ ConsultationType
	admin.request(t, "POST", "/api/v1/admin/consultation-types", ConsultationTypeInput{Title: "Visit", DurationMinutes: 60, Price: "25.00"}, 201, &typ)
	var offering Offering
	admin.request(t, "PUT", "/api/v1/admin/offerings", map[string]any{"consultation_type_id": typ.ID, "consultant_id": c.ID, "is_active": true}, 200, &offering)
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	intervalPath := fmt.Sprintf("/api/v1/consultants/%d/work-intervals", c.ID)
	var interval WorkInterval
	consultant.request(t, "POST", intervalPath, WorkIntervalInput{StartsAt: start, EndsAt: start.Add(4 * time.Hour)}, 201, &interval)
	client.request(t, "GET", intervalPath, nil, 403, nil)
	var slots []Slot
	rangeQuery := "?from=" + url.QueryEscape(start.Format(time.RFC3339)) + "&to=" + url.QueryEscape(start.Add(4*time.Hour).Format(time.RFC3339))
	other.request(t, "GET", fmt.Sprintf("/api/v1/offerings/%d/slots", offering.ID)+rangeQuery, nil, 200, &slots)
	if len(slots) == 0 {
		t.Fatal("no public slots")
	}
	var address Address
	client.request(t, "POST", "/api/v1/addresses", AddressInput{AddressLine: "123 Main Street"}, 201, &address)
	if address.Label != nil {
		t.Fatal("null optional label was not preserved")
	}
	var unlabeled Address
	client.request(t, "POST", "/api/v1/addresses", map[string]string{"address_line": "456 Main Street"}, 201, &unlabeled)
	if unlabeled.Label != nil {
		t.Fatal("omitted label was not optional")
	}
	deletePath := fmt.Sprintf("/api/v1/addresses/%d", unlabeled.ID)
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{"null", 400, "invalid_json"},
		{"[]", 400, "invalid_json"},
		{`{"password":"delete-secret"}`, 422, "invalid_request"},
	} {
		response := client.requestRaw(t, "DELETE", deletePath, tc.body, tc.status, nil)
		httpAssertSafeError(t, response, tc.code, "delete-secret")
	}
	client.request(t, "DELETE", deletePath, struct{}{}, 204, nil)
	addressPath := fmt.Sprintf("/api/v1/addresses/%d", address.ID)
	other.request(t, "GET", addressPath, nil, 404, nil)
	other.request(t, "PUT", addressPath, AddressInput{AddressLine: "Stolen"}, 404, nil)
	other.request(t, "DELETE", addressPath, struct{}{}, 404, nil)
	var booking Booking
	client.request(t, "POST", "/api/v1/bookings", BookingInput{OfferingID: offering.ID, AddressID: &address.ID, StartsAt: start}, 201, &booking)
	assertDetails := func(got Booking) {
		t.Helper()
		if got.ID != booking.ID || got.ConsultationTitle != typ.Title || got.ConsultantName != c.Name || got.ClientName == "" || got.AddressLine == nil || *got.AddressLine != address.AddressLine {
			t.Fatalf("booking details = %+v", got)
		}
	}
	assertDetails(booking)
	for i, addressField := range []map[string]any{{}, {"client_address_id": nil}} {
		in := map[string]any{"consultation_offering_id": offering.ID, "starts_at": start.Add(time.Duration(i+1) * time.Hour)}
		for key, value := range addressField {
			in[key] = value
		}
		var optional Booking
		client.request(t, "POST", "/api/v1/bookings", in, 201, &optional)
		if optional.AddressID != nil || optional.AddressLine != nil {
			t.Fatal("optional booking address was not nil")
		}
		client.request(t, "POST", fmt.Sprintf("/api/v1/bookings/%d/status", optional.ID), map[string]string{"status": "cancelled"}, 200, nil)
	}
	path := fmt.Sprintf("/api/v1/bookings/%d", booking.ID)
	for _, browser := range []*httpTestBrowser{client, consultant, admin} {
		var details Booking
		browser.request(t, "GET", path, nil, 200, &details)
		assertDetails(details)
	}
	other.request(t, "GET", path, nil, 404, nil)
	other.request(t, "GET", path+"/events", nil, 404, nil)
	other.request(t, "POST", path+"/status", map[string]string{"status": "cancelled"}, 404, nil)
	client.request(t, "POST", path+"/status", map[string]string{"status": "confirmed"}, 403, nil)
	consultant.request(t, "POST", path+"/status", map[string]string{"status": "confirmed"}, 403, nil)
	admin.request(t, "POST", path+"/status", map[string]string{"status": "confirmed"}, 200, &booking)
	if booking.Status != "confirmed" {
		t.Fatal("status not updated")
	}
	consultant.request(t, "DELETE", fmt.Sprintf("%s/%d", intervalPath, interval.ID), struct{}{}, 409, nil)
	client.request(t, "POST", path+"/status", map[string]string{"status": "cancelled"}, 200, &booking)
	client.request(t, "POST", path+"/status", map[string]string{"status": "cancelled"}, 409, nil)
	var events []BookingEvent
	client.request(t, "GET", path+"/events", nil, 200, &events)
	if len(events) != 3 || events[0].Status != "pending" || events[2].Status != "cancelled" {
		t.Fatal("wrong status history")
	}
	var bookings []Booking
	other.request(t, "GET", "/api/v1/bookings"+rangeQuery, nil, 200, &bookings)
	if len(bookings) != 0 {
		t.Fatal("foreign booking leaked in list")
	}
	client.request(t, "GET", "/api/v1/bookings", nil, 422, nil)
	for _, invalid := range []string{"0", "-1", "+1", "9223372036854775808"} {
		client.request(t, "GET", "/api/v1/bookings/"+url.PathEscape(invalid), nil, 422, nil)
	}
	client.request(t, "GET", "/api/v1/bookings/9223372036854775807", nil, 404, nil)
	consultant.request(t, "DELETE", fmt.Sprintf("%s/%d", intervalPath, interval.ID), struct{}{}, 204, nil)
	client.request(t, "POST", "/api/v1/addresses", map[string]string{"address_line": "Empty body delete"}, 201, &unlabeled)
	client.request(t, "DELETE", fmt.Sprintf("/api/v1/addresses/%d", unlabeled.ID), nil, 204, nil)
}

func TestHTTPRateBudgetsAndSQLTimeout(t *testing.T) {
	s, _ := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	s.Config.RateLimit = 1
	app := NewApp(s)
	httpDo(t, app, httptest.NewRequest("GET", "/api/v1/consultants", nil), 200)
	response := httpDo(t, app, httptest.NewRequest("GET", "/api/v1/offerings", nil), 429)
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("missing retry header")
	}
	s.Config.RateLimit = 10
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO ip_rate_limits(requested_ip,bucket,window_started_at,request_count)
		SELECT requested_ip,'verify',clock_timestamp(),10 FROM ip_rate_limits WHERE bucket='http'`); err != nil {
		t.Fatal(err)
	}
	browser := newHTTPBrowser(t, app)
	for _, path := range []string{"verify-email", "login/verify", "reset-password"} {
		for _, body := range []string{`{}`, `{"email":"unknown@http.test","code":"123456","password":"budget-secret",`} {
			response := browser.requestRaw(t, "POST", "/api/v1/auth/"+path, body, 429, nil)
			httpAssertSafeError(t, response, "rate_limited", "123456", "budget-secret")
			if response.Header.Get("Retry-After") == "" {
				t.Fatal("verification budget lost retry header")
			}
		}
	}
	// Exhausting the pool verifies the HTTP standard-context deadline reaches SQL.
	s.DB.SetMaxOpenConns(1)
	conn, err := s.DB.Connx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.Config.RequestTimeout = 25 * time.Millisecond
	app = NewApp(s)
	started := time.Now()
	httpDo(t, app, httptest.NewRequest("GET", "/healthz", nil), 503)
	if time.Since(started) > time.Second {
		t.Fatal("SQL ignored HTTP deadline")
	}
}

func TestHTTPAuthenticationBeforeValidation(t *testing.T) {
	s, _ := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	browser := newHTTPBrowser(t, NewApp(s))
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/v1/bookings/not-an-id", ""},
		{"GET", "/api/v1/bookings?from=invalid", ""},
		{"PATCH", "/api/v1/me", `{"name":123,"password":"auth-secret"}`},
		{"PATCH", "/api/v1/me", `{"name":"private-name","password":"auth-secret",`},
		{"POST", "/api/v1/bookings", `{}`},
		{"DELETE", "/api/v1/addresses/not-an-id", `{}`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := browser.requestRaw(t, tc.method, tc.path, tc.body, 401, nil)
			httpAssertSafeError(t, response, "unauthenticated", "auth-secret", "private-name")
		})
	}
}
